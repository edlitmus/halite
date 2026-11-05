package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// A node with a hub goes on converging when the hub goes away, and across a
// restart during the outage, on the pillar and tree it last received.
//
// Real binaries over a real socket, because each half of this was already
// claimed in prose and was false, and every unit in the path passed: the
// schedule fired, the file was in the cache, and the run failed. DIVERGENCE
// 5.577.
func TestAScheduledHighstateSurvivesAHubOutage(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and a node")
	}
	if runtime.GOOS == "windows" {
		t.Skip("stops the hub with a signal and uses Unix paths in the tree")
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
		"\n    - makedirs: True\n    - contents: \"{{ pillar.get('app:value', 'MISSING') }}\"\n")
	write("hub/pillar/top.sls", "base:\n  '*':\n    - app\n")
	write("hub/pillar/app.sls", "app:\n  value: first-canary\n")
	write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states]}\npillar_roots: {base: [%[2]s/hub/pillar]}\n", addr, dir))
	write("node/node.yaml", fmt.Sprintf("hub: %s\nnode_id: outage1\npki_dir: %[2]s/node/pki\nstate_dir: %[2]s/node/state\n"+
		"cache_dir: %[2]s/node/cache\nfile_roots: {base: [%[2]s/node/local]}\npillar_roots: {base: [%[2]s/node/localp]}\n"+
		"schedule:\n  hs:\n    function: state.apply\n    seconds: 1\n", addr, dir))

	hubArgs := []string{"--config", dir + "/hub/hub.yaml", "--root", dir + "/hub", "--pki-dir", dir + "/hub/pki"}
	nodeArgs := []string{"--config", dir + "/node/node.yaml", "--root", dir + "/node"}
	hubBin, nodeBin := filepath.Join(bin, "halite-hub"), filepath.Join(bin, "halite-node")
	start := func(name string, args ...string) (stop func()) {
		t.Helper()
		cmd := exec.Command(name, args...)
		var log bytes.Buffer
		cmd.Stdout, cmd.Stderr = &log, &log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		stopped := false
		stop = func() {
			if !stopped {
				stopped = true
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}
		t.Cleanup(stop)
		return stop
	}
	startHub := func() func() {
		return start(hubBin, append([]string{"serve", "--names", "127.0.0.1,localhost"}, hubArgs...)...)
	}
	waitMarker := func(want, when string) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		var got []byte
		for time.Now().Before(deadline) {
			got, _ = os.ReadFile(marker)
			if strings.TrimSpace(string(got)) == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s: the marker holds %q, want %q", when, got, want)
	}
	clearMarker := func() {
		t.Helper()
		if err := os.Remove(marker); err != nil {
			t.Fatal(err)
		}
	}

	stopHub := startHub()
	var fingerprint string
	for i := 0; i < 100 && fingerprint == ""; i++ {
		out, err := exec.Command(hubBin, append([]string{"keys", "fingerprint"}, hubArgs...)...).Output()
		if err == nil {
			fields := strings.Fields(string(out))
			if len(fields) > 0 {
				fingerprint = fields[len(fields)-1]
			}
		}
		if fingerprint == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if fingerprint == "" {
		t.Fatal("the hub never produced a fingerprint")
	}
	// **A fingerprint is not a listening hub.** `keys fingerprint` reads
	// the CA from disk, and `serve` writes the CA first and listens last,
	// after the serving certificate and everything else it sets up. The
	// node was started in that gap once in CI's fips-test job (run
	// 42077713188), was refused a connection fetching the CA, and exited,
	// as `enroll` does when the hub is unreachable; the loop below then
	// spent twenty seconds accepting a request nobody had made. The hub
	// is ready when it answers, so that is what is waited for.
	listening := false
	for i := 0; i < 200 && !listening; i++ {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if listening = err == nil; listening {
			c.Close()
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !listening {
		t.Fatalf("the hub never listened on %s", addr)
	}
	enroll := exec.Command(nodeBin, append([]string{"enroll", "--wait", "--hub-fingerprint", fingerprint}, nodeArgs...)...)
	var enrollLog syncBuffer
	enroll.Stdout, enroll.Stderr = &enrollLog, &enrollLog
	if err := enroll.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = enroll.Process.Kill() })
	enrolled := make(chan error, 1)
	go func() { enrolled <- enroll.Wait() }()
	// An enroll that has ended will never be accepted, and says why: stop
	// asking and say so, rather than asking for twenty seconds and then
	// blaming the acceptance.
	accepted := false
	for i := 0; i < 200 && !accepted; i++ {
		select {
		case err := <-enrolled:
			t.Fatalf("enroll ended before its request was accepted: %v\n%s", err, enrollLog.String())
		default:
		}
		accepted = exec.Command(hubBin, append([]string{"keys", "accept", "outage1"}, hubArgs...)...).Run() == nil
		if !accepted {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !accepted {
		t.Fatalf("the node's request was never accepted\n%s", enrollLog.String())
	}
	if err := <-enrolled; err != nil {
		t.Fatalf("enroll: %v\n%s", err, enrollLog.String())
	}

	stopNode := start(nodeBin, append([]string{"connect"}, nodeArgs...)...)
	waitMarker("first-canary", "with the hub up")

	// No plaintext copy of the pillar is what makes keeping it acceptable.
	store := filepath.Join(dir, "node", "state", "pillar-cache")
	entries, err := os.ReadDir(store)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no last known good pillar was kept: %v", err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(store, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("first-canary")) {
			t.Error("the pillar is on disk in plaintext")
		}
	}

	stopHub()
	clearMarker()
	waitMarker("first-canary", "with the hub down")

	stopNode()
	clearMarker()
	stopNode = start(nodeBin, append([]string{"connect"}, nodeArgs...)...)
	waitMarker("first-canary", "after a restart with the hub down")

	// The hub is the authority again as soon as it is back, in the same
	// process. A cache that outlived the outage would keep the old value.
	write("hub/pillar/app.sls", "app:\n  value: second-canary\n")
	startHub()
	waitMarker("second-canary", "with the hub back")
	stopNode()
}

// syncBuffer is a bytes.Buffer a child process writes to while the test
// reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
