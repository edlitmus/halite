package main

import (
	"context"
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

// A job that outlives its expiry stays partial when the node's return
// arrives, with real binaries (issue 810, DIVERGENCE 5.715).
//
// The hub settles an expired job `partial` and counts it in
// halite_jobs_expired_total, and a node that was still running it answers
// afterwards. The return is filed, because it is the only evidence of what
// ran, but the record must not move to `complete`: the counter, `jobs list`
// and every earlier answer would then disagree with it for ever. The unit
// test for this drives the hub's own handlers; this one drives the settle loop
// the hub really runs, which ticks once a minute, and a node that really
// takes the job and answers late.
//
// Slow by construction: the job's lifetime is five seconds, the settle tick
// is a minute, and the function must outlast the tick. About eighty seconds.
func TestAJobThatOutlivesItsExpiryStaysPartialWhenItsReturnArrives(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a hub and a node and waits through a settle tick")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the function is POSIX shell")
	}
	lab := startBinaryLab(t, "exp1")

	// The function runs for 75 s. The job's lifetime is 5 s, so it expires
	// while the node is running it, and the settle tick, at most 60 s after
	// the last one, lands inside the 70 s between the expiry and the return.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, lab.hubBin, append([]string{
		"run", "exp1", "cmd.run", "sleep 75; echo finished-late", "--ttl", "5s", "--timeout", "120s",
		"--as", "ed", "--out", "json"}, lab.hubArgs...)...)
	var runOutput syncBuffer
	run.Stdout, run.Stderr = &runOutput, &runOutput
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- run.Wait() }()

	jid := lab.waitForState(t, "partial", 100*time.Second)
	if got := lab.expiredCount(t); got != 1 {
		t.Errorf("halite_jobs_expired_total is %d once the job is settled partial, want 1", got)
	}
	select {
	case err := <-finished:
		t.Fatalf("the run ended before the node answered (%v); the function was meant to outlast the settle tick:\n%s",
			err, runOutput.String())
	default:
	}

	select {
	case <-finished:
	case <-time.After(60 * time.Second):
		t.Fatalf("the node's late return never reached the run:\n%s", runOutput.String())
	}
	if !strings.Contains(runOutput.String(), "finished-late") {
		t.Errorf("the run did not print the late return:\n%s", runOutput.String())
	}

	state, returns := lab.jobRecord(t, jid)
	if state != "partial" {
		t.Errorf("the job is %q after its late return, want it to stay partial", state)
	}
	if returns != 1 {
		t.Errorf("%d returns are filed against the job, want the late one", returns)
	}
	if got := lab.expiredCount(t); got != 1 {
		t.Errorf("halite_jobs_expired_total is %d after the late return, want 1", got)
	}
}

// binaryLab is a real hub and nodes over a real socket, each in its own
// directories, on a loopback port of its own. Nothing here touches the
// machine's own configuration, and the processes are stopped by handle,
// never by name.
type binaryLab struct {
	dir      string
	hubBin   string
	hubArgs  []string
	hubLog   syncBuffer
	nodeLog  syncBuffer
	operator func(args ...string) (string, error)
}

func startBinaryLab(t *testing.T, nodeIDs ...string) *binaryLab {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	for _, cmd := range []string{"halite-hub", "halite-node"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(bin, cmd),
			"github.com/edlitmus/halite/cmd/"+cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("building %s: %v\n%s", cmd, err, out)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states]}\npillar_roots: {base: [%[2]s/hub/pillar]}\n", addr, dir))
	write("hub/pillar/top.sls", "base: {}\n")

	lab := &binaryLab{
		dir:     dir,
		hubBin:  filepath.Join(bin, "halite-hub"),
		hubArgs: []string{"--config", dir + "/hub/hub.yaml", "--root", dir + "/hub", "--pki-dir", dir + "/hub/pki"},
	}
	nodeBin := filepath.Join(bin, "halite-node")

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
	startHub := func() func() {
		t.Helper()
		stop := start(&lab.hubLog, lab.hubBin,
			append([]string{"serve", "--names", "127.0.0.1,localhost"}, lab.hubArgs...)...)
		for i := 0; i < 200; i++ {
			if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
				c.Close()
				return stop
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("the hub never listened on %s\n%s", addr, lab.hubLog.String())
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
		t.Fatalf("timed out waiting for %s\nhub:\n%s\nnodes:\n%s", what, lab.hubLog.String(), lab.nodeLog.String())
	}
	lab.operator = func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, lab.hubBin, append(args, lab.hubArgs...)...).CombinedOutput()
		return string(out), err
	}

	stopHub := startHub()
	var fingerprint string
	waitFor("the hub's fingerprint", func() bool {
		out, err := exec.Command(lab.hubBin, append([]string{"keys", "fingerprint"}, lab.hubArgs...)...).Output()
		if fields := strings.Fields(string(out)); err == nil && len(fields) > 0 {
			fingerprint = fields[len(fields)-1]
		}
		return fingerprint != ""
	})
	if out, err := lab.operator("keys", "operator", "create", "ed", "--admin"); err != nil {
		t.Fatalf("creating the operator: %v\n%s", err, out)
	}
	// The policy `--admin` wrote is read when the hub starts.
	stopHub()
	startHub()

	for _, id := range nodeIDs {
		write(id+"/node.yaml", fmt.Sprintf("hub: %s\nnode_id: %s\npki_dir: %[3]s/%[2]s/pki\nstate_dir: %[3]s/%[2]s/state\n"+
			"cache_dir: %[3]s/%[2]s/cache\nfile_roots: {base: [%[3]s/%[2]s/local]}\npillar_roots: {base: [%[3]s/%[2]s/localp]}\n",
			addr, id, dir))
		nodeArgs := []string{"--config", dir + "/" + id + "/node.yaml", "--root", dir + "/" + id}
		enroll := exec.Command(nodeBin, append([]string{"enroll", "--wait", "--hub-fingerprint", fingerprint}, nodeArgs...)...)
		var enrollLog syncBuffer
		enroll.Stdout, enroll.Stderr = &enrollLog, &enrollLog
		if err := enroll.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = enroll.Process.Kill() })
		enrolled := make(chan error, 1)
		go func() { enrolled <- enroll.Wait() }()
		waitFor("the request of "+id+" to be accepted", func() bool {
			select {
			case err := <-enrolled:
				t.Fatalf("enroll ended before its request was accepted: %v\n%s", err, enrollLog.String())
			default:
			}
			_, err := lab.operator("keys", "accept", id)
			return err == nil
		})
		if err := <-enrolled; err != nil {
			t.Fatalf("enroll %s: %v\n%s", id, err, enrollLog.String())
		}
		start(&lab.nodeLog, nodeBin, append([]string{"connect"}, nodeArgs...)...)
	}
	waitFor("every node to answer", func() bool {
		out, err := lab.operator("run", "*", "test.ping", "--as", "ed", "--out", "json")
		return err == nil && strings.Count(out, "true") >= len(nodeIDs)
	})
	return lab
}

// listedJob is the part of a job record these tests read.
type listedJob struct {
	JID   string `json:"jid"`
	State string `json:"state"`
	Fun   string `json:"fun"`
}

// jobsRunning is the job cache's records of one function, read from its
// files as `jobs list` does.
func (lab *binaryLab) jobsRunning(t *testing.T, fun string) []listedJob {
	t.Helper()
	out, err := lab.operator("jobs", "list", "--limit", "50", "--out", "json")
	if err != nil {
		t.Fatalf("jobs list: %v\n%s", err, out)
	}
	var all []listedJob
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		t.Fatalf("jobs list printed something that is not JSON: %v\n%s", err, out)
	}
	var picked []listedJob
	for _, j := range all {
		if j.Fun == fun {
			picked = append(picked, j)
		}
	}
	return picked
}

// waitForState waits for the one `cmd.run` job to read as the state, and
// returns its jid.
func (lab *binaryLab) waitForState(t *testing.T, want string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	last := "no cmd.run job yet"
	for time.Now().Before(deadline) {
		if jobs := lab.jobsRunning(t, "cmd.run"); len(jobs) == 1 {
			last = jobs[0].State
			if last == want {
				return jobs[0].JID
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("the job never read %q within %s; it read %q\nhub:\n%s", want, within, last, lab.hubLog.String())
	return ""
}

func (lab *binaryLab) jobRecord(t *testing.T, jid string) (state string, returns int) {
	t.Helper()
	out, err := lab.operator("jobs", "show", jid, "--out", "json")
	if err != nil {
		t.Fatalf("jobs show: %v\n%s", err, out)
	}
	var shown struct {
		Job     struct{ State string } `json:"job"`
		Returns []json.RawMessage      `json:"returns"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("jobs show printed something that is not JSON: %v\n%s", err, out)
	}
	return shown.Job.State, len(shown.Returns)
}

// expiredCount reads halite_jobs_expired_total from the running hub.
func (lab *binaryLab) expiredCount(t *testing.T) int {
	t.Helper()
	out, err := lab.operator("metrics")
	if err != nil {
		t.Fatalf("metrics: %v\n%s", err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "halite_jobs_expired_total") {
			var n int
			if _, err := fmt.Sscanf(strings.Fields(line)[len(strings.Fields(line))-1], "%d", &n); err != nil {
				t.Fatalf("cannot read the counter from %q", line)
			}
			return n
		}
	}
	t.Fatalf("halite_jobs_expired_total is not in the exposition:\n%s", out)
	return 0
}
