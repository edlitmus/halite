package main

import (
	"encoding/json"
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

// A resume after the hub died mid-run does not run again the steps that had
// already run on the nodes.
//
// The run's record was written when the run started and when it ended, and at
// no point between, so a hub killed between the two left a record naming no
// steps at all. A resume seeds only the steps the record names, so every step
// before the resume point "had not run" and ran again: `orch resume <jid>
// --from step3` sent step1 and step2 to the node a second time. Harmless for a
// state, and not for the `cmd.run` that initialises a database. Issue 802,
// DIVERGENCE 5.705.
//
// Real binaries over a real socket, and the hub is killed rather than stopped:
// a crash, an OOM kill and a power cut give the hub no chance to write
// anything on the way out, which is the case a resume exists for.
func TestAResumeAfterAHubCrashDoesNotRepeatFinishedSteps(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and a node")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the steps are POSIX shell and the hub is killed with a signal")
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
	marks := filepath.Join(dir, "out", "marks")
	release := filepath.Join(dir, "out", "release")
	if err := os.MkdirAll(filepath.Dir(marks), 0o755); err != nil {
		t.Fatal(err)
	}
	step := func(id, script string) string {
		return fmt.Sprintf("%s:\n  salt.function:\n    - name: cmd.run\n    - tgt: orch1\n    - arg:\n      - %q\n", id, script)
	}
	// step3 says it started and then holds until the test releases it, so
	// that the hub can be killed with step3 in flight and steps 1 and 2
	// known to have returned: the hub dispatches step3 only after step2's
	// return is in.
	write("hub/states/deploy.sls",
		step("step1", "echo step1 >> "+marks)+
			step("step2", "echo step2 >> "+marks)+
			step("step3", "echo step3-started >> "+marks+"; until [ -f "+release+" ]; do sleep 0.2; done; echo step3 >> "+marks)+
			step("step4", "echo step4 >> "+marks))
	write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states]}\npillar_roots: {base: [%[2]s/hub/pillar]}\n", addr, dir))
	write("hub/pillar/top.sls", "base: {}\n")
	write("node/node.yaml", fmt.Sprintf("hub: %s\nnode_id: orch1\npki_dir: %[2]s/node/pki\nstate_dir: %[2]s/node/state\n"+
		"cache_dir: %[2]s/node/cache\nfile_roots: {base: [%[2]s/node/local]}\npillar_roots: {base: [%[2]s/node/localp]}\n", addr, dir))

	hubArgs := []string{"--config", dir + "/hub/hub.yaml", "--root", dir + "/hub", "--pki-dir", dir + "/hub/pki"}
	nodeArgs := []string{"--config", dir + "/node/node.yaml", "--root", dir + "/node"}
	hubBin, nodeBin := filepath.Join(bin, "halite-hub"), filepath.Join(bin, "halite-node")
	start := func(log *syncBuffer, name string, args ...string) (stop func()) {
		t.Helper()
		cmd := exec.Command(name, args...)
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
	var hubLog, nodeLog syncBuffer
	startHub := func() func() {
		t.Helper()
		stop := start(&hubLog, hubBin, append([]string{"serve", "--names", "127.0.0.1,localhost"}, hubArgs...)...)
		for i := 0; i < 200; i++ {
			if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
				c.Close()
				return stop
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("the hub never listened on %s\n%s", addr, hubLog.String())
		return nil
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s\nhub:\n%s\nnode:\n%s", what, hubLog.String(), nodeLog.String())
	}
	counts := func() map[string]int {
		raw, _ := os.ReadFile(marks)
		out := map[string]int{}
		for _, line := range strings.Fields(string(raw)) {
			out[line]++
		}
		return out
	}
	operator := func(args ...string) (string, error) {
		out, err := exec.Command(hubBin, append(args, hubArgs...)...).CombinedOutput()
		return string(out), err
	}

	stopHub := startHub()
	var fingerprint string
	waitFor("the hub's fingerprint", func() bool {
		out, err := exec.Command(hubBin, append([]string{"keys", "fingerprint"}, hubArgs...)...).Output()
		if fields := strings.Fields(string(out)); err == nil && len(fields) > 0 {
			fingerprint = fields[len(fields)-1]
		}
		return fingerprint != ""
	})
	if out, err := operator("keys", "operator", "create", "ed", "--admin"); err != nil {
		t.Fatalf("creating the operator: %v\n%s", err, out)
	}
	// The policy `--admin` wrote is read when the hub starts.
	stopHub()
	stopHub = startHub()
	enroll := exec.Command(nodeBin, append([]string{"enroll", "--wait", "--hub-fingerprint", fingerprint}, nodeArgs...)...)
	var enrollLog syncBuffer
	enroll.Stdout, enroll.Stderr = &enrollLog, &enrollLog
	if err := enroll.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = enroll.Process.Kill() })
	enrolled := make(chan error, 1)
	go func() { enrolled <- enroll.Wait() }()
	waitFor("the node's request to be accepted", func() bool {
		select {
		case err := <-enrolled:
			t.Fatalf("enroll ended before its request was accepted: %v\n%s", err, enrollLog.String())
		default:
		}
		_, err := operator("keys", "accept", "orch1")
		return err == nil
	})
	if err := <-enrolled; err != nil {
		t.Fatalf("enroll: %v\n%s", err, enrollLog.String())
	}
	start(&nodeLog, nodeBin, append([]string{"connect"}, nodeArgs...)...)
	nodeAnswers := func() bool {
		out, err := operator("run", "orch1", "test.ping", "--as", "ed", "--out", "json")
		return err == nil && strings.Contains(out, "true")
	}
	waitFor("the node to answer", nodeAnswers)

	var orchLog syncBuffer
	start(&orchLog, hubBin, append([]string{"orch", "run", "deploy", "--as", "ed"}, hubArgs...)...)
	waitFor("step3 to start on the node", func() bool { return counts()["step3-started"] == 1 })

	stopHub()
	// step3 finishes on the node while the hub is down; its return is
	// retried, and the hub is back well inside the node's ten seconds of retries.
	write("out/release", "")
	waitFor("step3 to finish on the node", func() bool { return counts()["step3"] == 1 })
	startHub()
	// A step dispatched before the node is back finds it not connected;
	// that is a different question from this one.
	waitFor("the node to answer the restarted hub", nodeAnswers)

	var interrupted string
	waitFor("the interrupted run to be listed", func() bool {
		out, err := operator("orch", "list", "--as", "ed", "--out", "json")
		if err != nil {
			return false
		}
		var listed map[string]json.RawMessage
		if json.Unmarshal([]byte(out), &listed) != nil || len(listed) != 1 {
			return false
		}
		for jid := range listed {
			interrupted = jid
		}
		return true
	})
	shown, _ := operator("orch", "show", interrupted, "--as", "ed")
	t.Logf("the interrupted run's record after the restart:\n%s", shown)
	// The record says what happened before the hub died: two steps done,
	// the third out on the fleet. A run killed between its start and its
	// end used to record no steps at all.
	for _, want := range []string{"ok   step1", "ok   step2", "wait step3"} {
		if !strings.Contains(shown, want) {
			t.Errorf("the interrupted run's record has no %q", want)
		}
	}

	// Resuming at the step that was in flight runs that step and the ones
	// after it, and nothing before it.
	out, err := operator("orch", "resume", interrupted, "--from", "step3", "--as", "ed")
	if err != nil {
		t.Fatalf("orch resume --from step3: %v\n%s\nhub:\n%s\nnode:\n%s", err, out, hubLog.String(), nodeLog.String())
	}
	got := counts()
	if got["step1"] != 1 || got["step2"] != 1 {
		t.Errorf("a resume --from step3 ran the earlier steps again: step1 ran %d time(s), step2 %d, want 1 each\n%s",
			got["step1"], got["step2"], out)
	}
	if got["step3"] != 2 || got["step4"] != 1 {
		t.Errorf("a resume --from step3 should run step3 again and step4 once: step3 %d, step4 %d\n%s",
			got["step3"], got["step4"], out)
	}

	// Resuming after it: step3's job finished on the node and its return
	// reached the restarted hub, so the job record says it ran, and it is
	// carried forward rather than sent again.
	waitFor("step3's first job to have its return", func() bool {
		out, err = operator("orch", "resume", interrupted, "--from", "step4", "--as", "ed")
		if err != nil {
			t.Logf("orch resume --from step4 refused, for now: %v\n%s", err, out)
			return false
		}
		return true
	})
	t.Logf("orch resume --from step4:\n%s", out)
	if !strings.Contains(out, "in flight when the hub stopped") {
		t.Errorf("the resume does not say step3 was carried forward from its job record:\n%s", out)
	}
	got = counts()
	if got["step1"] != 1 || got["step2"] != 1 || got["step3"] != 2 || got["step3-started"] != 2 {
		t.Errorf("a resume --from step4 ran earlier steps again: %v", got)
	}
	if got["step4"] != 2 {
		t.Errorf("a resume --from step4 ran step4 %d time(s) in all, want 2 (one per resume)", got["step4"])
	}
}
