package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Issue 820 through the real binaries: `halite-hub run <node> state.apply
// pillar=...`, unsigned, merges the override over the node's pillar for
// that one run, the way Salt's state.apply does -- a mapping merged
// recursively, so a nested key the override does not name survives -- and
// the next run without one sees the ordinary pillar again.
//
// It also holds the two things an override must not do. It is not a
// pillar read as far as quiesce is concerned: an override saying
// `quiesce: true` neither quiesces the node nor leaves the record that
// would keep it quiesced across a restart, and one saying `quiesce: false`
// does not lift a quiesce the compiled pillar set. And an override that is
// not a mapping fails the job, rather than being dropped and the state
// applied with the ordinary pillar, which is what Salt does (it logs
// "Pillar override was not passed as a dictionary" and carries on).
//
// The node wrote the ordinary pillar in every case before the fix: the
// job carried `pillar` and nothing on the node read it. DIVERGENCE 5.712.
func TestAStateJobsPillarOverrideAppliesToThatRunOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and a node")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix paths in the tree")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	for _, cmd := range []string{"halite-hub", "halite-node"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(bin, cmd),
			"github.com/edlitmus/halite/cmd/"+cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("building %s: %v\n%s", cmd, err, out)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(dir, "out", "marker")
	write("hub/states/top.sls", "base:\n  '*':\n    - app\n")
	write("hub/states/app.sls", "marker:\n  file.managed:\n    - name: "+marker+
		"\n    - makedirs: True\n    - contents: \"{{ pillar.get('app_version', 'MISSING') }} "+
		"{{ pillar['nested']['keep'] }} {{ pillar['nested']['over'] }} "+
		"{{ pillar.get('only_override', 'none') }} quiesce={{ pillar.get('quiesce', 'unset') }}\"\n")
	write("hub/pillar/top.sls", "base:\n  '*':\n    - app\n")
	ordinaryPillar := "app_version: from-pillar\nnested:\n  keep: kept\n  over: base\n"
	write("hub/pillar/app.sls", ordinaryPillar)
	write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states]}\npillar_roots: {base: [%[2]s/hub/pillar]}\n", addr, dir))
	// No pillar cache on disk, so every job asks the hub, and a change to
	// the hub's pillar is the next job's pillar.
	write("node/node.yaml", fmt.Sprintf("hub: %s\nnode_id: over1\npki_dir: %[2]s/node/pki\nstate_dir: %[2]s/node/state\n"+
		"cache_dir: %[2]s/node/cache\nfile_roots: {base: [%[2]s/node/local]}\npillar_roots: {base: [%[2]s/node/localp]}\n"+
		"pillar_cache_disk: false\n", addr, dir))

	hubArgs := []string{"--config", dir + "/hub/hub.yaml", "--root", dir + "/hub", "--pki-dir", dir + "/hub/pki"}
	nodeArgs := []string{"--config", dir + "/node/node.yaml", "--root", dir + "/node"}
	hubBin, nodeBin := filepath.Join(bin, "halite-hub"), filepath.Join(bin, "halite-node")
	start := func(log *syncBuffer, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	}
	hub := func(args ...string) (string, int) {
		t.Helper()
		out, err := exec.Command(hubBin, append(args, hubArgs...)...).CombinedOutput()
		if err != nil {
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("%v: %v", args, err)
			}
			return string(out), exitErr.ExitCode()
		}
		return string(out), 0
	}

	// The first serve makes the enrollment CA the operator's certificate
	// is issued from, and the hub reads the policy `--admin` writes only
	// when it starts, so it is started again after.
	serve := append([]string{"serve", "--names", "127.0.0.1,localhost"}, hubArgs...)
	var firstLog syncBuffer
	first := exec.Command(hubBin, serve...)
	first.Stdout, first.Stderr = &firstLog, &firstLog
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(filepath.Join(dir, "hub", "pki", "ca.crt")); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = first.Process.Kill()
	_ = first.Wait()
	if out, code := hub("keys", "operator", "create", "ops", "--admin"); code != 0 {
		t.Fatalf("keys operator create: %s\nfirst serve:\n%s", out, firstLog.String())
	}
	var hubLog, nodeLog syncBuffer
	start(&hubLog, hubBin, serve...)
	for i := 0; i < 200; i++ {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var fingerprint string
	for i := 0; i < 100 && fingerprint == ""; i++ {
		out, err := exec.Command(hubBin, append([]string{"keys", "fingerprint"}, hubArgs...)...).Output()
		if fields := strings.Fields(string(out)); err == nil && len(fields) > 0 {
			fingerprint = fields[len(fields)-1]
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	enroll := exec.Command(nodeBin, append([]string{"enroll", "--wait", "--hub-fingerprint", fingerprint}, nodeArgs...)...)
	var enrollLog syncBuffer
	enroll.Stdout, enroll.Stderr = &enrollLog, &enrollLog
	if err := enroll.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = enroll.Process.Kill() })
	for i := 0; i < 200; i++ {
		if exec.Command(hubBin, append([]string{"keys", "accept", "over1"}, hubArgs...)...).Run() == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := enroll.Wait(); err != nil {
		t.Fatalf("enroll: %v\n%s", err, enrollLog.String())
	}
	start(&nodeLog, nodeBin, append([]string{"connect"}, nodeArgs...)...)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if out, _ := hub("runner", "manage.up"); strings.Contains(out, "over1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node never connected:\n%s", tail(nodeLog.String(), 12))
		}
		time.Sleep(200 * time.Millisecond)
	}

	record := filepath.Join(dir, "node", "state", pillarQuiesceFile)
	apply := func(extra ...string) (string, int) {
		t.Helper()
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return hub(append([]string{"run", "over1", "state.apply"}, extra...)...)
	}
	wantMarker := func(when, want string) {
		t.Helper()
		got, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("%s: no marker: %v\nnode:\n%s", when, err, tail(nodeLog.String(), 12))
		}
		if strings.TrimSpace(string(got)) != want {
			t.Fatalf("%s: the marker holds %q, want %q\nnode:\n%s", when, got, want, tail(nodeLog.String(), 12))
		}
	}

	// The ordinary pillar, as the control.
	if out, code := apply(); code != 0 {
		t.Fatalf("the plain state.apply failed:\n%s", out)
	}
	wantMarker("without an override", "from-pillar kept base none quiesce=unset")

	// The override, merged: a key it replaces, a nested key it replaces
	// beside one it leaves, and a key only it has.
	if out, code := apply(`pillar={"app_version": "9.9", "nested": {"over": "job"}, "only_override": "yes-it-is"}`); code != 0 {
		t.Fatalf("state.apply with a pillar override failed:\n%s\nnode:\n%s", out, tail(nodeLog.String(), 12))
	}
	wantMarker("with an override", "9.9 kept job yes-it-is quiesce=unset")

	// An override saying quiesce is data the run renders with, and not
	// the node's pillar saying so.
	if out, code := apply(`pillar={"quiesce": true, "quiesce_allowlist": ["test.ping"]}`); code != 0 {
		t.Fatalf("state.apply with a quiesce override failed:\n%s\nnode:\n%s", out, tail(nodeLog.String(), 12))
	}
	wantMarker("with a quiesce override", "from-pillar kept base none quiesce=True")
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("an override was recorded as the pillar quiescing the node: %v\nnode:\n%s", err, tail(nodeLog.String(), 12))
	}
	if strings.Contains(nodeLog.String(), "the pillar has quiesced this node") {
		t.Fatalf("the node took an override for its pillar quiescing it:\n%s", tail(nodeLog.String(), 12))
	}

	// And the next run without one has the ordinary pillar back, and is
	// not quiesced by the last run's override.
	if out, code := apply(); code != 0 {
		t.Fatalf("the plain state.apply after the overrides failed:\n%s\nnode:\n%s", out, tail(nodeLog.String(), 12))
	}
	wantMarker("after the overrides", "from-pillar kept base none quiesce=unset")

	// Not a mapping: refused, saying why, and nothing applied.
	out, code := apply("pillar=just-a-string")
	if code == 0 {
		t.Fatalf("state.apply with a string for a pillar override succeeded:\n%s", out)
	}
	if !strings.Contains(out, "pillar override") || !strings.Contains(out, "mapping") {
		t.Errorf("the refusal does not say the override must be a mapping:\n%s", out)
	}
	t.Logf("a string for an override: %s", strings.TrimSpace(out))
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a state applied under an override that was not a mapping: %v", err)
	}

	// The hub's pillar quiesces the node, and the next job's override says
	// it does not. Nothing has read the new pillar yet -- the string
	// override above was refused before any pillar was compiled -- so the
	// job is admitted against the old one, and the refusal has to come
	// from the pillar it compiles itself (quiesceAfterPillar). That is the
	// check an override merged before the quiesce is read would get past;
	// a node that already knew would refuse at admission, before an
	// override is looked at, and would prove nothing about it.
	write("hub/pillar/app.sls", ordinaryPillar+"quiesce: true\nquiesce_allowlist: [test.ping]\n")
	out, code = apply(`pillar={"quiesce": false, "quiesce_allowlist": ["state.apply"]}`)
	if code == 0 {
		t.Fatalf("an override lifted the pillar's quiesce:\n%s\nnode:\n%s", out, tail(nodeLog.String(), 12))
	}
	if !strings.Contains(out, "quiesced") {
		t.Errorf("the refusal does not say the node is quiesced:\n%s", out)
	}
	t.Logf("quiesce: false in an override, the job's own pillar quiescing: %s", strings.TrimSpace(out))
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a state applied on a quiesced node because its override said quiesce: false: %v", err)
	}
	if !strings.Contains(nodeLog.String(), "the pillar has quiesced this node") {
		t.Fatalf("the node did not record the quiescing pillar:\n%s", tail(nodeLog.String(), 12))
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("no record of the pillar's quiesce: %v", err)
	}
}
