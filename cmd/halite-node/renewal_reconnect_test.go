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

// After a renewal a running node goes on converging, on the new
// certificate: its next highstate fetches pillar and files with it.
//
// It did not. The agent took the pillar fetcher and the file server's
// client from the first connection and kept them, so once a renewal
// revoked the old serial every highstate was refused ("this node's
// enrollment is revoked: superseded by a renewal") until the agent was
// restarted, and the refusal deleted the node's last known good pillar on
// the way. The subscribe stream reconnected on the new certificate, so the
// node looked connected. Found driving the 0.1.1 and 0.2.0 release
// binaries through a rolling upgrade on FreeBSD (issue 766), and the same
// on main.
//
// Both ways a renewal happens: by hand, `halite-node renew --force`, which
// the handbook gives as the way to rotate a key that may have leaked; and
// by the agent itself at half its certificate's life, which every node
// does on schedule and which is why this is not an edge case.
func TestAHighstateAfterARenewalUsesTheNewCertificate(t *testing.T) {
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
	t.Run("renewed by hand", func(t *testing.T) {
		r := newRenewalLab(t, bin, "")
		r.converge("first")
		if out, err := exec.Command(r.nodeBin, append([]string{"renew", "--force"}, r.nodeArgs...)...).CombinedOutput(); err != nil {
			t.Fatalf("renew: %v\n%s", err, out)
		}
		r.converge("after a renewal by hand")
	})
	t.Run("renewed by the agent", func(t *testing.T) {
		// A short life, so the agent's own half-life renewal comes round
		// inside the test: it checks every twentieth of the life.
		r := newRenewalLab(t, bin, "certificate_lifetime: 40s\n")
		r.converge("first")
		r.waitForLog("renewed this node's certificate", 60*time.Second)
		r.converge("after the agent renewed itself")
	})
}

type renewalLab struct {
	t                 *testing.T
	dir, marker       string
	hubBin, nodeBin   string
	hubArgs, nodeArgs []string
	nodeLog           *syncBuffer
	round             int
}

func newRenewalLab(t *testing.T, bin, hubExtra string) *renewalLab {
	t.Helper()
	dir := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	r := &renewalLab{
		t: t, dir: dir, marker: filepath.Join(dir, "out", "marker"),
		hubBin: filepath.Join(bin, "halite-hub"), nodeBin: filepath.Join(bin, "halite-node"),
		hubArgs:  []string{"--config", dir + "/hub/hub.yaml", "--root", dir + "/hub", "--pki-dir", dir + "/hub/pki"},
		nodeArgs: []string{"--config", dir + "/node/node.yaml", "--root", dir + "/node"},
	}
	r.write("hub/states/top.sls", "base:\n  '*':\n    - app\n")
	r.write("hub/states/app.sls", "marker:\n  file.managed:\n    - name: "+r.marker+
		"\n    - makedirs: True\n    - contents: \"{{ pillar.get('app:value', 'MISSING') }}\"\n")
	r.write("hub/pillar/top.sls", "base:\n  '*':\n    - app\n")
	r.write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states]}\npillar_roots: {base: [%[2]s/hub/pillar]}\n%[3]s",
		addr, dir, hubExtra))
	r.write("node/node.yaml", fmt.Sprintf("hub: %s\nnode_id: renew1\npki_dir: %[2]s/node/pki\nstate_dir: %[2]s/node/state\n"+
		"cache_dir: %[2]s/node/cache\nfile_roots: {base: [%[2]s/node/local]}\npillar_roots: {base: [%[2]s/node/localp]}\n"+
		"schedule:\n  hs:\n    function: state.apply\n    seconds: 1\n", addr, dir))

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
	// A fingerprint is not a listening hub; see the outage test.
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
		if exec.Command(r.hubBin, append([]string{"keys", "accept", "renew1"}, r.hubArgs...)...).Run() == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := enroll.Wait(); err != nil {
		t.Fatalf("enroll: %v\n%s", err, enrollLog.String())
	}
	r.nodeLog = &syncBuffer{}
	r.start(r.nodeBin, r.nodeLog, append([]string{"connect"}, r.nodeArgs...)...)
	return r
}

func (r *renewalLab) write(rel, body string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *renewalLab) start(name string, log *syncBuffer, args ...string) {
	r.t.Helper()
	cmd := exec.Command(name, args...)
	if log == nil {
		log = &syncBuffer{}
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
}

// converge changes the pillar and waits for the scheduled highstate to
// write the new value, which needs both a pillar fetch and a file fetch
// to succeed.
func (r *renewalLab) converge(when string) {
	r.t.Helper()
	r.round++
	want := fmt.Sprintf("round-%d", r.round)
	r.write("hub/pillar/app.sls", "app:\n  value: "+want+"\n")
	deadline := time.Now().Add(45 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(r.marker)
		if strings.TrimSpace(string(got)) == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.t.Fatalf("%s: the marker holds %q, want %q; the node's log ends:\n%s", when, got, want, tail(r.nodeLog.String(), 12))
}

func (r *renewalLab) waitForLog(line string, within time.Duration) {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if strings.Contains(r.nodeLog.String(), line) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	r.t.Fatalf("the node never logged %q within %s:\n%s", line, within, tail(r.nodeLog.String(), 12))
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
