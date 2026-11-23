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

// Issue 826 through the real binaries: `halite-hub run <node> state.apply
// pillarenv=<other>` renders that run's states with the pillar compiled
// from the named pillar environment, as Salt's state.apply does, and the
// next run without one has the node's own pillar environment again.
//
// It also holds what the named environment must not do. Its pillar is not
// the node's pillar for quiesce (5.703): an environment whose pillar says
// `quiesce: true` neither quiesces the node nor leaves the record, and one
// saying `quiesce: false` does not lift a quiesce the node's own pillar
// set. And the node's env_denylist governs a job's pillarenv as it governs
// the node's configured one, so a job cannot read pillar from an
// environment the node may not use.
//
// Before the fix every run wrote the node's own pillar: the job carried
// `pillarenv` and nothing on the node read it. DIVERGENCE 5.716.
func TestAStateJobsPillarenvCompilesThatRunsPillarFromIt(t *testing.T) {
	lab := startPillarenvLab(t, "hub")

	// The node's own pillar environment, as the control.
	if out, code := lab.apply(); code != 0 {
		t.Fatalf("the plain state.apply failed:\n%s", out)
	}
	lab.wantMarker("without a pillarenv", "from-base none quiesce=unset")

	// The other environment's pillar, alone: not merged over base's.
	if out, code := lab.apply("pillarenv=other"); code != 0 {
		t.Fatalf("state.apply pillarenv=other failed:\n%s\nnode:\n%s", out, lab.nodeTail())
	}
	lab.wantMarker("with pillarenv=other", "from-other yes-it-is quiesce=unset")

	// A pillarenv and a pillar override together: the override is merged
	// over the named environment's pillar, as in Salt.
	if out, code := lab.apply("pillarenv=other", `pillar={"app_version": "9.9"}`); code != 0 {
		t.Fatalf("state.apply pillarenv=other with an override failed:\n%s\nnode:\n%s", out, lab.nodeTail())
	}
	lab.wantMarker("with pillarenv=other and an override", "9.9 yes-it-is quiesce=unset")

	// An environment whose pillar says quiesce: data the run renders
	// with, and not the node's pillar saying so.
	if out, code := lab.apply("pillarenv=loud"); code != 0 {
		t.Fatalf("state.apply pillarenv=loud failed:\n%s\nnode:\n%s", out, lab.nodeTail())
	}
	lab.wantMarker("with pillarenv=loud", "from-loud none quiesce=True")
	if _, err := os.Stat(lab.record); !os.IsNotExist(err) {
		t.Fatalf("another environment's pillar was recorded as quiescing the node: %v\nnode:\n%s", err, lab.nodeTail())
	}
	if strings.Contains(lab.nodeLog.String(), "the pillar has quiesced this node") {
		t.Fatalf("the node took another environment's pillar for its own quiescing it:\n%s", lab.nodeTail())
	}

	// The next run without one has the node's own environment back.
	if out, code := lab.apply(); code != 0 {
		t.Fatalf("the plain state.apply after the pillarenv runs failed:\n%s\nnode:\n%s", out, lab.nodeTail())
	}
	lab.wantMarker("after the pillarenv runs", "from-base none quiesce=unset")

	// An environment the node's env_denylist excludes: refused, naming
	// the control, and nothing applied.
	out, code := lab.apply("pillarenv=secret")
	if code == 0 {
		t.Fatalf("state.apply pillarenv=secret succeeded on a node that denies it:\n%s", out)
	}
	if !strings.Contains(out, "env_denylist") {
		t.Errorf("the refusal does not name env_denylist:\n%s", out)
	}
	t.Logf("a denied pillarenv: %s", strings.TrimSpace(out))
	lab.wantNoMarker("with a denied pillarenv")

	// The node's own pillar quiesces it, and the job names an environment
	// whose pillar says it does not. As in the override test, nothing has
	// read the new pillar yet, so the job is admitted and the refusal
	// must come from the pillar the run compiles for the node itself.
	lab.write("hub/pillar/base/app.sls", lab.basePillar+"quiesce: true\nquiesce_allowlist: [test.ping]\n")
	out, code = lab.apply("pillarenv=calm")
	if code == 0 {
		t.Fatalf("another environment's pillar lifted the node's quiesce:\n%s\nnode:\n%s", out, lab.nodeTail())
	}
	if !strings.Contains(out, "quiesced") {
		t.Errorf("the refusal does not say the node is quiesced:\n%s", out)
	}
	t.Logf("pillarenv=calm on a node its own pillar quiesces: %s", strings.TrimSpace(out))
	lab.wantNoMarker("on a quiesced node")
	if _, err := os.Stat(lab.record); err != nil {
		t.Fatalf("no record of the node's own pillar's quiesce: %v", err)
	}
}

// The same on a hub that compiles no pillar, where the node compiles from
// its own pillar_roots: the probe at connect is told so, hubPillar is
// never set, and the run's environment is compiled locally on a copy.
//
// It does not reach the other way a node gets there, the fetcher's own
// fallback when the probe failed for another reason and a later fetch
// is told the hub compiles no pillar. Breaking that fallback back to the
// agent's environment left this test passing, which is how that was
// found; that path is read, not run. DIVERGENCE 5.716.
func TestAStateJobsPillarenvReachesTheNodesOwnRootsOnAHubWithoutPillar(t *testing.T) {
	lab := startPillarenvLab(t, "node")
	if out, code := lab.apply(); code != 0 {
		t.Fatalf("the plain state.apply failed:\n%s\nnode:\n%s", out, lab.nodeTail())
	}
	lab.wantMarker("without a pillarenv", "from-base none quiesce=unset")
	if !strings.Contains(lab.nodeLog.String(), "the hub compiles no pillar") {
		t.Fatalf("the node never fell back to its own roots, so this tests nothing:\n%s", lab.nodeTail())
	}
	if out, code := lab.apply("pillarenv=other"); code != 0 {
		t.Fatalf("state.apply pillarenv=other failed:\n%s\nnode:\n%s", out, lab.nodeTail())
	}
	lab.wantMarker("with pillarenv=other", "from-other yes-it-is quiesce=unset")
	if out, code := lab.apply("pillarenv=loud"); code != 0 {
		t.Fatalf("state.apply pillarenv=loud failed:\n%s\nnode:\n%s", out, lab.nodeTail())
	}
	lab.wantMarker("with pillarenv=loud", "from-loud none quiesce=True")
	if _, err := os.Stat(lab.record); !os.IsNotExist(err) {
		t.Fatalf("another environment's pillar was recorded as quiescing the node: %v\nnode:\n%s", err, lab.nodeTail())
	}
}

// pillarenvLab is a hub and one enrolled node, `penv1`, whose pillar
// environment is `base` alone and whose env_denylist is `secret`. The
// pillar trees -- one environment per case, each with a top file naming
// its own environment, as Salt's are -- are on the hub when pillarSide
// is "hub", and on the node, with a hub that compiles no pillar, when it
// is "node".
type pillarenvLab struct {
	t          *testing.T
	dir        string
	marker     string
	record     string
	basePillar string
	nodeLog    *syncBuffer
	hub        func(args ...string) (string, int)
}

func startPillarenvLab(t *testing.T, pillarSide string) *pillarenvLab {
	t.Helper()
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

	lab := &pillarenvLab{
		t:          t,
		dir:        dir,
		marker:     filepath.Join(dir, "out", "marker"),
		record:     filepath.Join(dir, "node", "state", pillarQuiesceFile),
		basePillar: "app_version: from-base\n",
		nodeLog:    &syncBuffer{},
	}
	lab.write("hub/states/top.sls", "base:\n  '*':\n    - app\n")
	lab.write("hub/states/app.sls", "marker:\n  file.managed:\n    - name: "+lab.marker+
		"\n    - makedirs: True\n    - contents: \"{{ pillar.get('app_version', 'MISSING') }} "+
		"{{ pillar.get('only_other', 'none') }} quiesce={{ pillar.get('quiesce', 'unset') }}\"\n")
	pillars := map[string]string{
		"base":   lab.basePillar,
		"other":  "app_version: from-other\nonly_other: yes-it-is\n",
		"calm":   "app_version: from-calm\nquiesce: false\nquiesce_allowlist: [state.apply]\n",
		"loud":   "app_version: from-loud\nquiesce: true\nquiesce_allowlist: [test.ping]\n",
		"secret": "app_version: from-secret\n",
	}
	var roots []string
	for env, body := range pillars {
		lab.write(pillarSide+"/pillar/"+env+"/top.sls", env+":\n  '*':\n    - app\n")
		lab.write(pillarSide+"/pillar/"+env+"/app.sls", body)
		roots = append(roots, fmt.Sprintf("%s: [%s/%s/pillar/%s]", env, dir, pillarSide, env))
	}
	pillarRoots := "pillar_roots: {" + strings.Join(roots, ", ") + "}\n"
	hubPillarRoots, nodePillarRoots := pillarRoots, fmt.Sprintf("pillar_roots: {base: [%s/node/localp]}\n", dir)
	if pillarSide == "node" {
		hubPillarRoots, nodePillarRoots = "", pillarRoots
	}
	lab.write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states]}\n", addr, dir)+hubPillarRoots)
	// No pillar cache on disk, so every job asks the hub.
	lab.write("node/node.yaml", fmt.Sprintf("hub: %s\nnode_id: penv1\npki_dir: %[2]s/node/pki\nstate_dir: %[2]s/node/state\n"+
		"cache_dir: %[2]s/node/cache\nfile_roots: {base: [%[2]s/node/local]}\n"+
		"pillarenv: base\nenv_denylist: [secret]\npillar_cache_disk: false\n", addr, dir)+nodePillarRoots)

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
	lab.hub = func(args ...string) (string, int) {
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

	// As in TestAStateJobsPillarOverrideAppliesToThatRunOnly: the first
	// serve makes the CA, and the policy `--admin` writes is read at start.
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
	if out, code := lab.hub("keys", "operator", "create", "ops", "--admin"); code != 0 {
		t.Fatalf("keys operator create: %s\nfirst serve:\n%s", out, firstLog.String())
	}
	var hubLog syncBuffer
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
		if exec.Command(hubBin, append([]string{"keys", "accept", "penv1"}, hubArgs...)...).Run() == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := enroll.Wait(); err != nil {
		t.Fatalf("enroll: %v\n%s", err, enrollLog.String())
	}
	start(lab.nodeLog, nodeBin, append([]string{"connect"}, nodeArgs...)...)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if out, _ := lab.hub("runner", "manage.up"); strings.Contains(out, "penv1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node never connected:\n%s", lab.nodeTail())
		}
		time.Sleep(200 * time.Millisecond)
	}
	return lab
}

func (l *pillarenvLab) write(rel, body string) {
	l.t.Helper()
	p := filepath.Join(l.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		l.t.Fatal(err)
	}
}

func (l *pillarenvLab) nodeTail() string { return tail(l.nodeLog.String(), 12) }

func (l *pillarenvLab) apply(extra ...string) (string, int) {
	l.t.Helper()
	if err := os.Remove(l.marker); err != nil && !os.IsNotExist(err) {
		l.t.Fatal(err)
	}
	return l.hub(append([]string{"run", "penv1", "state.apply"}, extra...)...)
}

func (l *pillarenvLab) wantMarker(when, want string) {
	l.t.Helper()
	got, err := os.ReadFile(l.marker)
	if err != nil {
		l.t.Fatalf("%s: no marker: %v\nnode:\n%s", when, err, l.nodeTail())
	}
	if strings.TrimSpace(string(got)) != want {
		l.t.Fatalf("%s: the marker holds %q, want %q\nnode:\n%s", when, got, want, l.nodeTail())
	}
}

func (l *pillarenvLab) wantNoMarker(when string) {
	l.t.Helper()
	if _, err := os.Stat(l.marker); !os.IsNotExist(err) {
		l.t.Fatalf("%s: a state applied: %v", when, err)
	}
}
