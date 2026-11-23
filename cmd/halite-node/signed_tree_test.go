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

	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/jobsign"
)

// Issue 764 end to end, with the shipped binaries: `halite-hub tree
// manifest` and `tree sign` sign the tree the hub serves, a real
// halite-node with `require_state_signature: true` applies it on its
// schedule, and then the hub is made to lie in each of the ways the
// design is for -- a file changed after signing, an older validly signed
// revision put back, and a stray file in `_halite/`. Each is refused, and
// the marker the state writes shows what was applied.
func TestASignedTreeIsAppliedAndATamperedOrOlderOneIsNot(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and a node")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix paths in the tree")
	}
	bin := t.TempDir()
	for _, cmd := range []string{"halite-hub", "halite-node"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(bin, cmd),
			"github.com/edlitmus/halite/cmd/"+cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("building %s: %v\n%s", cmd, err, out)
		}
	}
	r, keyPath := newSignedTreeLab(t, bin)
	states := filepath.Join(r.dir, "hub", "states")
	sign := func(when string) {
		t.Helper()
		for _, args := range [][]string{
			{"tree", "manifest", states, "--env", "base", "--source", when},
			{"tree", "sign", states, "--sign-key", keyPath},
			{"tree", "verify", states, "--env", "base", "--keys", filepath.Join(r.dir, "trusted")},
		} {
			out, err := exec.Command(r.hubBin, append(args, "--config", r.dir+"/hub/hub.yaml")...).CombinedOutput()
			if err != nil {
				t.Fatalf("%s: halite-hub %s: %v\n%s", when, strings.Join(args, " "), err, out)
			}
		}
	}
	waitMarker := func(want, when string) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		var got []byte
		for time.Now().Before(deadline) {
			got, _ = os.ReadFile(r.marker)
			if strings.TrimSpace(string(got)) == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s: the marker holds %q, want %q; the node's log ends:\n%s", when, got, want, tail(r.nodeLog.String(), 12))
	}
	holdsFor := func(want string, d time.Duration) {
		t.Helper()
		time.Sleep(d)
		if got, _ := os.ReadFile(r.marker); strings.TrimSpace(string(got)) != want {
			t.Fatalf("the marker changed to %q while the node was meant to refuse the tree", got)
		}
	}

	// Signed, and applied.
	r.write("hub/states/app/marker.txt", "v1\n")
	sign("first")
	r.startNode()
	waitMarker("v1", "the first signed revision")
	r.waitForLog("signed state content: accepted a new revision", 10*time.Second)
	firstManifest, _ := os.ReadFile(filepath.Join(states, "_halite", "manifest.json"))
	firstSig, _ := os.ReadFile(filepath.Join(states, "_halite", "manifest.sig"))

	// The hub changes a file after it was signed: refused, by name.
	r.write("hub/states/app/marker.txt", "evil\n")
	r.waitForLog("app/marker.txt arrived with sha256", 30*time.Second)
	holdsFor("v1", 2*time.Second)
	// `tree verify` says the same before any node does.
	out, err := exec.Command(r.hubBin, "tree", "verify", states, "--env", "base",
		"--keys", filepath.Join(r.dir, "trusted"), "--config", r.dir+"/hub/hub.yaml").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "app/marker.txt: its content differs from the manifest's") {
		t.Fatalf("tree verify on a changed tree: %v\n%s", err, out)
	}

	// Restored, and a new revision signed: applied.
	r.write("hub/states/app/marker.txt", "v2\n")
	sign("second")
	waitMarker("v2", "the second signed revision")

	// The hub puts the first revision back, validly signed and older.
	r.write("hub/states/app/marker.txt", "v1\n")
	r.write("hub/states/_halite/manifest.json", string(firstManifest))
	r.write("hub/states/_halite/manifest.sig", string(firstSig))
	r.waitForLog("refusing an older revision", 30*time.Second)
	holdsFor("v2", 2*time.Second)

	// And a stray file in _halite/: the hub refuses the environment.
	r.write("hub/states/app/marker.txt", "v3\n")
	sign("third")
	waitMarker("v3", "the third signed revision")
	r.write("hub/states/_halite/notes.txt", "x\n")
	r.waitForLog("_halite holds notes.txt", 30*time.Second)
}

func newSignedTreeLab(t *testing.T, bin string) (*signedTreeLab, string) {
	t.Helper()
	dir := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	r := &signedTreeLab{renewalLab: &renewalLab{
		t: t, dir: dir, marker: filepath.Join(dir, "out", "marker"),
		hubBin: filepath.Join(bin, "halite-hub"), nodeBin: filepath.Join(bin, "halite-node"),
		hubArgs:  []string{"--config", dir + "/hub/hub.yaml", "--root", dir + "/hub", "--pki-dir", dir + "/hub/pki"},
		nodeArgs: []string{"--config", dir + "/node/node.yaml", "--root", dir + "/node"},
	}}

	key, err := jobsign.GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, _ := jobsign.EncodePrivateKey(key)
	keyPath := filepath.Join(dir, "tree-ci.key")
	if err := fileperm.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	line, _ := jobsign.FormatSignerKey("tree-ci", &key.PublicKey)
	r.write("trusted", line+"\n")

	r.write("hub/states/top.sls", "base:\n  '*':\n    - app\n")
	r.write("hub/states/app/init.sls", "marker:\n  file.managed:\n    - name: "+r.marker+
		"\n    - makedirs: True\n    - source: salt://app/marker.txt\n")
	r.write("hub/pillar/top.sls", "base: {}\n")
	r.write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states]}\npillar_roots: {base: [%[2]s/hub/pillar]}\n",
		addr, dir))
	r.write("node/node.yaml", fmt.Sprintf("hub: %s\nnode_id: signed1\npki_dir: %[2]s/node/pki\nstate_dir: %[2]s/node/state\n"+
		"cache_dir: %[2]s/node/cache\nfile_roots: {base: [%[2]s/node/local]}\npillar_roots: {base: [%[2]s/node/localp]}\n"+
		"require_state_signature: true\nstate_signer_keys:\n  - %q\n"+
		"schedule:\n  hs:\n    function: state.apply\n    seconds: 1\n", addr, dir, line))

	r.start(r.hubBin, nil, append([]string{"serve", "--names", "127.0.0.1,localhost"}, r.hubArgs...)...)
	fingerprint := ""
	for i := 0; i < 100 && fingerprint == ""; i++ {
		if out, err := exec.Command(r.hubBin, append([]string{"keys", "fingerprint"}, r.hubArgs...)...).Output(); err == nil {
			if fields := strings.Fields(string(out)); len(fields) > 0 {
				fingerprint = fields[len(fields)-1]
			}
		}
		if fingerprint == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	for i := 0; i < 200; i++ {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	enroll := exec.Command(r.nodeBin, append([]string{"enroll", "--wait", "--hub-fingerprint", fingerprint}, r.nodeArgs...)...)
	var enrollLog syncBuffer
	enroll.Stdout, enroll.Stderr = &enrollLog, &enrollLog
	if err := enroll.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = enroll.Process.Kill() })
	for i := 0; i < 200; i++ {
		if exec.Command(r.hubBin, append([]string{"keys", "accept", "signed1"}, r.hubArgs...)...).Run() == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := enroll.Wait(); err != nil {
		t.Fatalf("enroll: %v\n%s", err, enrollLog.String())
	}
	return r, keyPath
}

type signedTreeLab struct {
	*renewalLab
}

func (r *signedTreeLab) startNode() {
	r.nodeLog = &syncBuffer{}
	r.start(r.nodeBin, r.nodeLog, append([]string{"connect"}, r.nodeArgs...)...)
}
