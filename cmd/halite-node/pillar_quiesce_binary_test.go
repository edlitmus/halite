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

// A hub's pillar quiesces a node, a failed fetch and a restart with the
// hub away do not lift it, the pillar lifts its own, and it cannot lift
// the node's own. Real binaries over a real socket, with the node's
// schedule applying a state that writes a file every second, so "the
// node ran nothing" is a file that does not appear. DIVERGENCE 5.703.
//
// `pillar_cache_disk: false`, so that with the hub away the node has no
// last known good pillar to answer from and the record of the pillar's
// quiesce is the only thing that remembers it. With the cache on, the
// cached pillar would say "quiesced" itself and the record would not be
// what was tested.
func TestAPillarQuiesceHoldsThroughAHubOutageAndLiftsOnlyItsOwn(t *testing.T) {
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
		"\n    - makedirs: True\n    - contents: \"quiesce={{ pillar.get('quiesce', 'unset') }}\"\n")
	write("hub/pillar/top.sls", "base:\n  '*':\n    - app\n")
	write("hub/pillar/app.sls", "app: {}\n")
	write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states]}\npillar_roots: {base: [%[2]s/hub/pillar]}\n", addr, dir))
	nodeConfig := fmt.Sprintf("hub: %s\nnode_id: quiet1\npki_dir: %[2]s/node/pki\nstate_dir: %[2]s/node/state\n"+
		"cache_dir: %[2]s/node/cache\nfile_roots: {base: [%[2]s/node/local]}\npillar_roots: {base: [%[2]s/node/localp]}\n"+
		"pillar_cache_disk: false\n"+
		"schedule:\n  hs:\n    function: state.apply\n    seconds: 1\n", addr, dir)
	write("node/node.yaml", nodeConfig)

	hubArgs := []string{"--config", dir + "/hub/hub.yaml", "--root", dir + "/hub", "--pki-dir", dir + "/hub/pki"}
	nodeArgs := []string{"--config", dir + "/node/node.yaml", "--root", dir + "/node"}
	hubBin, nodeBin := filepath.Join(bin, "halite-hub"), filepath.Join(bin, "halite-node")
	start := func(log *syncBuffer, name string, args ...string) (stop func()) {
		t.Helper()
		cmd := exec.Command(name, args...)
		if log == nil {
			log = &syncBuffer{}
		}
		cmd.Stdout, cmd.Stderr = log, log
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
		stop := start(nil, hubBin, append([]string{"serve", "--names", "127.0.0.1,localhost"}, hubArgs...)...)
		for i := 0; i < 200; i++ {
			if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
				c.Close()
				return stop
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("the hub never listened on %s", addr)
		return nil
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	markerExists := func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}
	clearMarker := func() {
		t.Helper()
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	// Five schedule ticks with no file is the node running nothing; the
	// log line is what proves the ticks happened rather than the schedule
	// having stopped.
	wantQuiet := func(when string, log *syncBuffer) {
		t.Helper()
		clearMarker()
		before := strings.Count(log.String(), "not running a scheduled job")
		time.Sleep(5 * time.Second)
		if markerExists() {
			t.Fatalf("%s: the scheduled state ran on a quiesced node\n%s", when, log.String())
		}
		if after := strings.Count(log.String(), "not running a scheduled job"); after <= before {
			t.Fatalf("%s: the schedule did not fire, so its silence proves nothing\n%s", when, log.String())
		}
	}
	record := filepath.Join(dir, "node", "state", pillarQuiesceFile)

	stopHub := startHub()
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
	enrolled := make(chan error, 1)
	go func() { enrolled <- enroll.Wait() }()
	accepted := false
	for i := 0; i < 200 && !accepted; i++ {
		select {
		case err := <-enrolled:
			t.Fatalf("enroll ended before its request was accepted: %v\n%s", err, enrollLog.String())
		default:
		}
		accepted = exec.Command(hubBin, append([]string{"keys", "accept", "quiet1"}, hubArgs...)...).Run() == nil
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

	// 1. Not quiesced: the schedule writes the file.
	var nodeLog syncBuffer
	stopNode := start(&nodeLog, nodeBin, append([]string{"connect"}, nodeArgs...)...)
	waitFor("the first scheduled run", markerExists)

	// 2. The pillar quiesces it, and the next run is the first to know:
	// it was admitted against the pillar before, and its own pillar has to
	// stop it. The state writes what its pillar said, so a run that went
	// ahead on the quiescing pillar would leave `quiesce=True` behind.
	write("hub/pillar/app.sls", "quiesce: true\nquiesce_allowlist: [test.ping]\n")
	waitFor("the node to log the pillar's quiesce", func() bool {
		return strings.Contains(nodeLog.String(), "the pillar has quiesced this node")
	})
	time.Sleep(2 * time.Second)
	if got, _ := os.ReadFile(marker); strings.Contains(string(got), "quiesce=True") {
		t.Fatalf("the run that read the quiescing pillar applied the state anyway\n%s", nodeLog.String())
	}
	wantQuiet("quiesced by the pillar", &nodeLog)
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("no record of the pillar's quiesce: %v", err)
	}
	doctor, _ := exec.Command(nodeBin, append([]string{"doctor"}, nodeArgs...)...).CombinedOutput()
	if !strings.Contains(string(doctor), "quiesced by its pillar's quiesce; runs only test.ping") {
		t.Errorf("doctor does not say the pillar quiesced the node:\n%s", doctor)
	}
	t.Logf("doctor's quiesce line: %s", lineWith(string(doctor), "quiesce "))

	// 3. The hub goes away, and the node restarts while it is away. There
	// is no cached pillar, so only the record remembers.
	stopHub()
	wantQuiet("with the hub down", &nodeLog)
	stopNode()
	var restartLog syncBuffer
	stopNode = start(&restartLog, nodeBin, append([]string{"connect"}, nodeArgs...)...)
	waitFor("the restarted node to say it is still quiesced", func() bool {
		return strings.Contains(restartLog.String(), "stays quiesced until a pillar says otherwise")
	})
	wantQuiet("after a restart with the hub down", &restartLog)

	// 4. The hub returns with the key gone from the pillar: the pillar
	// lifts its own quiesce, in the same process.
	write("hub/pillar/app.sls", "app: {}\n")
	clearMarker()
	startHub()
	waitFor("the scheduled state to run once the pillar lifted quiesce", markerExists)
	if !strings.Contains(restartLog.String(), "the pillar no longer quiesces this node") {
		t.Errorf("the node did not log the lift\n%s", restartLog.String())
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("the record outlived the quiesce: %v", err)
	}

	// 5. The node's own quiesce, and a pillar that says otherwise.
	stopNode()
	write("node/node.yaml", nodeConfig+"quiesce: true\nquiesce_allowlist: [test.ping, state.apply]\n")
	write("hub/pillar/app.sls", "quiesce: false\nquiesce_allowlist: [state.apply]\n")
	var localLog syncBuffer
	stopNode = start(&localLog, nodeBin, append([]string{"connect"}, nodeArgs...)...)
	// The configuration allowlists state.apply, so the schedule runs; now
	// the pillar quiesces with a list that leaves it out, and the
	// intersection refuses it.
	waitFor("the locally quiesced node to run its allowlisted state", markerExists)
	write("hub/pillar/app.sls", "quiesce: true\nquiesce_allowlist: [test.ping]\n")
	waitFor("the node to log the pillar's quiesce", func() bool {
		return strings.Contains(localLog.String(), "the pillar has quiesced this node")
	})
	wantQuiet("quiesced by both, with lists that share only test.ping", &localLog)
	// And a pillar saying quiesce is off cannot take state.apply off the
	// configuration's list or put anything else on it: it lifts only its
	// own part.
	write("hub/pillar/app.sls", "quiesce: false\n")
	clearMarker()
	waitFor("the pillar's own quiesce to lift", markerExists)
	if !strings.Contains(localLog.String(), "the pillar no longer quiesces this node; its configuration still does") {
		t.Errorf("the lift does not say the configuration still quiesces the node\n%s", localLog.String())
	}
	stopNode()
	write("node/node.yaml", nodeConfig+"quiesce: true\nquiesce_allowlist: [test.ping]\n")
	write("hub/pillar/app.sls", "quiesce: false\nquiesce_allowlist: [state.apply]\n")
	var lastLog syncBuffer
	stopNode = start(&lastLog, nodeBin, append([]string{"connect"}, nodeArgs...)...)
	waitFor("the node to start", func() bool { return strings.Contains(lastLog.String(), "this node is quiesced") })
	wantQuiet("quiesced by its configuration, with a pillar that says it is not", &lastLog)
	stopNode()
}

func lineWith(text, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
