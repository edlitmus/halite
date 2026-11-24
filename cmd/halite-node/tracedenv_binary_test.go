package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Issue 803 through the real binaries: a job the node's tracer samples,
// naming an environment other than the node's own, compiles that
// environment's states and fetches its `salt://` files from the hub's
// tree -- the same as the same job does on a node that traces nothing.
//
// The suspicion was that the wrapper which turns each fetch into a span
// hides the hub's file server from adoptJobEnvironment, whose
// `n.files.(*fileserver.Remote)` would then fail and send the job to the
// node's own roots. So each node here has its own roots for `dev` as
// well, holding a decoy whose state and file both say so: a job that
// lost the hub's tree writes "from-node-local" rather than failing, and
// that is the silent shape the issue describes. (Without the decoy, the
// node's own roots for `dev` would be the packaged defaults, which on a
// developer machine can be a live tree.)
//
// The traced node's tracer samples every job (`tracing_sample_rate: 1`)
// and exports to a collector this test runs, which is how the test knows
// the traced path ran: the collector must receive a `file fetch` span
// naming `dev`. A tracer that sampled nothing would leave the wrapper
// out, and the assertion on the file would pass for the wrong reason.
func TestATracedJobForAnotherEnvironmentFetchesFromTheHubsTree(t *testing.T) {
	lab := startTracedEnvLab(t)

	for _, node := range []string{"plain1", "traced1"} {
		out, code := lab.hub("run", node, "state.apply", "app", "--env", "dev")
		if code != 0 {
			t.Fatalf("%s: state.apply app --env dev failed:\n%s\nnode:\n%s", node, out, tail(lab.nodeLogs[node].String(), 12))
		}
		got, err := os.ReadFile(filepath.Join(lab.dir, "out", node))
		if err != nil {
			t.Fatalf("%s: no marker: %v\n%s\nnode:\n%s", node, err, out, tail(lab.nodeLogs[node].String(), 12))
		}
		if strings.TrimSpace(string(got)) != "from-hub-dev" {
			t.Fatalf("%s: a job for dev wrote %q, want the hub's dev tree's file (from-hub-dev)\nnode:\n%s",
				node, got, tail(lab.nodeLogs[node].String(), 12))
		}
	}

	// The traced node's run really was traced, and its fetch of the
	// payload was for dev: otherwise the loop above proved nothing about
	// the wrapper.
	deadline := time.Now().Add(20 * time.Second)
	for !lab.collector.saw(`"file fetch"`, `"stringValue":"dev"`, "payload.txt") {
		if time.Now().After(deadline) {
			t.Fatalf("the collector never received a traced fetch of dev's payload, so the traced path did not run:\n%s",
				tail(lab.collector.String(), 5))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lab.collector.saw("plain1") {
		t.Errorf("the untraced node exported spans, so it is not the control it is meant to be")
	}
}

// spanCollector is an OTLP/HTTP endpoint that keeps every body it is sent.
type spanCollector struct {
	mu     sync.Mutex
	bodies []string
}

func (c *spanCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.bodies = append(c.bodies, string(body))
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *spanCollector) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.bodies, "\n")
}

// saw reports whether one exported batch holds every one of the strings.
func (c *spanCollector) saw(parts ...string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, body := range c.bodies {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(body, p)
		}
		if all {
			return true
		}
	}
	return false
}

type tracedEnvLab struct {
	dir       string
	collector *spanCollector
	nodeLogs  map[string]*syncBuffer
	hub       func(args ...string) (string, int)
}

// startTracedEnvLab is a hub serving `base` and `dev`, and two enrolled
// nodes whose own environment is `base`: `traced1`, which samples every
// job, and `plain1`, which traces nothing.
func startTracedEnvLab(t *testing.T) *tracedEnvLab {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs a hub and two nodes")
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

	collector := &spanCollector{}
	server := httptest.NewServer(collector)
	t.Cleanup(server.Close)

	lab := &tracedEnvLab{dir: dir, collector: collector, nodeLogs: map[string]*syncBuffer{}}
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
	state := func(source string) string {
		return "marker:\n  file.managed:\n    - name: " + dir + "/out/{{ grains['id'] }}\n" +
			"    - makedirs: True\n    - source: " + source + "\n"
	}
	write("hub/states/base/app.sls", state("salt://payload.txt"))
	write("hub/states/base/payload.txt", "from-hub-base\n")
	write("hub/states/dev/app.sls", state("salt://payload.txt"))
	write("hub/states/dev/payload.txt", "from-hub-dev\n")
	write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states/base], dev: [%[2]s/hub/states/dev]}\n", addr, dir))

	nodes := map[string]string{
		"plain1":  "",
		"traced1": "tracing: otlp\ntracing_endpoint: " + server.URL + "/v1/traces\ntracing_sample_rate: 1\n",
	}
	for id, tracing := range nodes {
		root := "node-" + id
		write(root+"/local/base/app.sls", state("salt://payload.txt"))
		write(root+"/local/base/payload.txt", "from-node-local\n")
		write(root+"/local/dev/app.sls", state("salt://payload.txt"))
		write(root+"/local/dev/payload.txt", "from-node-local\n")
		write(root+"/node.yaml", fmt.Sprintf("hub: %s\nnode_id: %s\npki_dir: %[3]s/pki\nstate_dir: %[3]s/state\n"+
			"cache_dir: %[3]s/cache\nfile_roots: {base: [%[3]s/local/base], dev: [%[3]s/local/dev]}\n"+
			"pillar_roots: {base: [%[3]s/pillar]}\n", addr, id, filepath.Join(dir, root))+tracing)
	}

	hubArgs := []string{"--config", dir + "/hub/hub.yaml", "--root", dir + "/hub", "--pki-dir", dir + "/hub/pki"}
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

	// As in startPillarenvLab: the first serve makes the CA, and the
	// policy `--admin` writes is read at start.
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
	for id := range nodes {
		root := filepath.Join(dir, "node-"+id)
		nodeArgs := []string{"--config", root + "/node.yaml", "--root", root}
		enroll := exec.Command(nodeBin, append([]string{"enroll", "--wait", "--hub-fingerprint", fingerprint}, nodeArgs...)...)
		var enrollLog syncBuffer
		enroll.Stdout, enroll.Stderr = &enrollLog, &enrollLog
		if err := enroll.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = enroll.Process.Kill() })
		for i := 0; i < 200; i++ {
			if exec.Command(hubBin, append([]string{"keys", "accept", id}, hubArgs...)...).Run() == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err := enroll.Wait(); err != nil {
			t.Fatalf("enroll %s: %v\n%s", id, err, enrollLog.String())
		}
		lab.nodeLogs[id] = &syncBuffer{}
		start(lab.nodeLogs[id], nodeBin, append([]string{"connect"}, nodeArgs...)...)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, _ := lab.hub("runner", "manage.up")
		if strings.Contains(out, "plain1") && strings.Contains(out, "traced1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the nodes never connected:\n%s", out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return lab
}
