package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Issue 815: `orch run` and `orch resume` waited for the run's outcome on
// one request through the operator client, whose timeout is thirty
// seconds. A longer run -- a database initialisation, a data migration --
// made the command fail with "context deadline exceeded" while the run
// carried on on the hub: the operator was told it had failed, and never
// got the result.
//
// A real hub, and the real command, with the operator client's timeout
// cut to one second (operatorTimeoutEnv, read only by this test binary's
// TestMain) so that a run held for three seconds by an event it waits for
// outlasts every request the command makes.
func TestOrchRunWaitsForARunLongerThanTheClientsTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a hub and waits several seconds for an orchestration")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix paths in the tree")
	}
	lab := newOrchWaitLab(t)

	released := lab.releaseAfter(3 * time.Second)
	started := time.Now()
	got := lab.hub("orch", "run", "gate")
	took := time.Since(started)
	<-released
	t.Logf("orch run took %s and exited %d:\n%s%s", took.Round(time.Millisecond), got.code, got.stdout, got.stderr)

	if got.code != 0 {
		t.Fatalf("orch run exited %d for a run that succeeded after %s; the client's timeout is 1s:\n%s%s",
			got.code, took.Round(time.Millisecond), got.stdout, got.stderr)
	}
	if took < 2500*time.Millisecond {
		t.Fatalf("orch run returned after %s, before the run could have finished", took)
	}
	for _, want := range []string{"complete", "ok   wait_for_go", "ok   announce"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the timeline does not say %q:\n%s", want, got.stdout)
		}
	}
	jid := runningJID(t, got.stderr)
	if !strings.HasPrefix(got.stdout, jid+"  complete") {
		t.Errorf("the timeline is not the run the command announced, %s:\n%s", jid, got.stdout)
	}
}

// A run that fails after the client's timeout is reported as failed, with
// its timeline, and exits 1: the exit status still reflects the outcome.
func TestOrchRunReportsALongRunsFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a hub and waits several seconds for an orchestration")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix paths in the tree")
	}
	lab := newOrchWaitLab(t)

	got := lab.hub("orch", "run", "timesout")
	t.Logf("orch run exited %d:\n%s%s", got.code, got.stdout, got.stderr)
	if got.code != 1 {
		t.Fatalf("orch run exited %d for a run whose step failed after 3s, want 1:\n%s%s", got.code, got.stdout, got.stderr)
	}
	jid := runningJID(t, got.stderr)
	if !strings.Contains(got.stdout, "FAIL never") {
		t.Errorf("the failed step is not in the timeline:\n%s", got.stdout)
	}
	if want := "orchestration " + jid + " failed"; !strings.Contains(got.stderr, want) {
		t.Errorf("stderr does not say %q:\n%s", want, got.stderr)
	}

	// `orch resume` waits the same way: the resumed step fails again,
	// three seconds in, and that is what it reports.
	resumed := lab.hub("orch", "resume", jid, "--from", "never")
	t.Logf("orch resume exited %d:\n%s%s", resumed.code, resumed.stdout, resumed.stderr)
	if resumed.code != 1 {
		t.Fatalf("orch resume exited %d for a resumed step that failed after 3s, want 1:\n%s%s",
			resumed.code, resumed.stdout, resumed.stderr)
	}
	again := runningJID(t, resumed.stderr)
	if want := "resumed " + jid + " from step never"; !strings.Contains(resumed.stdout, want) ||
		!strings.Contains(resumed.stdout, "FAIL never") || again == jid {
		t.Errorf("the resume's timeline does not say %q and the step's failure:\n%s", want, resumed.stdout)
	}
}

// A run refused before its first step is the answer, as it always was:
// nothing is said to be running, and the refusal exits 1.
func TestOrchRunReportsARefusalAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a hub")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix paths in the tree")
	}
	lab := newOrchWaitLab(t)

	got := lab.hub("orch", "run", "nosuchorchestration")
	t.Logf("orch run exited %d:\n%s%s", got.code, got.stdout, got.stderr)
	if got.code != 1 || strings.Contains(got.stderr, "is running on the hub") ||
		!strings.Contains(got.stderr, "nosuchorchestration") {
		t.Fatalf("an orchestration that does not exist was not refused by name, at once:\n%s%s", got.stdout, got.stderr)
	}
}

// The handbook's `deployer` role grants `state.orchestrate` and nothing
// else. Waiting reads the runner call's own job, which is held to the
// grant that started it, so that role sees its run's outcome; polling
// `state.orch_show` instead would have refused it.
func TestARoleThatMayOnlyOrchestrateSeesALongRunsOutcome(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a hub and waits several seconds for an orchestration")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix paths in the tree")
	}
	lab := newOrchWaitLab(t)

	released := lab.releaseAfter(3 * time.Second)
	got := lab.hub("orch", "run", "gateonly", "--as", "pipeline")
	<-released
	t.Logf("orch run --as pipeline exited %d:\n%s%s", got.code, got.stdout, got.stderr)
	if got.code != 0 || !strings.Contains(got.stdout, "ok   wait_for_go") {
		t.Fatalf("a role that may orchestrate did not see its run's outcome:\n%s%s", got.stdout, got.stderr)
	}
	// The control: the same role may not read the run by `orch show`, so a
	// wait that polled it would have been refused.
	jid := runningJID(t, got.stderr)
	if show := lab.hub("orch", "show", jid, "--as", "pipeline"); show.code == 0 {
		t.Fatalf("the deployer role may call state.orch_show, so this test shows nothing:\n%s", show.stdout)
	} else {
		t.Logf("orch show --as pipeline, refused as it should be:\n%s", show.stderr)
	}
}

// Interrupted while it waits, the command says the run is still running
// and how to look, and exits 3 -- not 1, which is a run that failed. The
// run is unaffected and finishes, and `orch show` has it.
func TestAnInterruptedOrchRunLeavesTheRunRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a hub and waits several seconds for an orchestration")
	}
	if runtime.GOOS == "windows" {
		t.Skip("no interrupt to send to another process")
	}
	lab := newOrchWaitLab(t)

	cmd := lab.command("orch", "run", "gate")
	var stdout, stderr syncBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(stderr.String(), "is running on the hub") {
		if time.Now().After(deadline) {
			t.Fatalf("orch run never said the run had started:\n%s%s", stdout.String(), stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := cmd.Wait(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = exitErr.ExitCode()
	}
	t.Logf("interrupted orch run exited %d:\n%s%s", code, stdout.String(), stderr.String())
	if code != 3 {
		t.Errorf("an interrupted wait exited %d, want 3", code)
	}
	jid := runningJID(t, stderr.String())
	if want := "orch show " + jid; !strings.Contains(stderr.String(), "still running") ||
		!strings.Contains(stderr.String(), want) {
		t.Errorf("the interruption does not say the run is still running and to %q:\n%s", want, stderr.String())
	}

	if show := lab.hub("orch", "show", jid); !strings.HasPrefix(show.stdout, jid+"  running") {
		t.Fatalf("the run is not running after the command was interrupted:\n%s%s", show.stdout, show.stderr)
	}
	<-lab.releaseAfter(0)
	deadline = time.Now().Add(20 * time.Second)
	for {
		show := lab.hub("orch", "show", jid)
		if strings.HasPrefix(show.stdout, jid+"  complete") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run never finished once released:\n%s%s", show.stdout, show.stderr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// A hub that restarts while a run is going stops the run, and closes the
// runner call that started it as aborted (Server.AbandonRunnerJobs). A wait
// that outlives the restart says so and exits 3, rather than polling for
// ever a job nothing will answer.
func TestAWaitThatOutlivesAHubRestartSaysTheRunStopped(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a hub twice")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix paths in the tree")
	}
	lab := newOrchWaitLab(t)

	cmd := lab.command("orch", "run", "gate")
	var stdout, stderr syncBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(stderr.String(), "is running on the hub") {
		if time.Now().After(deadline) {
			t.Fatalf("orch run never said the run had started:\n%s%s", stdout.String(), stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	lab.restartHub()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var err error
	select {
	case err = <-exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("orch run was still waiting 30s after the hub restarted:\n%s%s", stdout.String(), stderr.String())
	}
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	t.Logf("orch run across a restart exited %d:\n%s%s", code, stdout.String(), stderr.String())
	jid := runningJID(t, stderr.String())
	if code != 3 || !strings.Contains(stderr.String(), "the hub stopped while orchestration "+jid+" was running") {
		t.Fatalf("the wait did not say the hub stopped the run, and exit 3:\n%s", stderr.String())
	}
}

// runningJID is the run the command announced on stderr before it waited.
func runningJID(t *testing.T, stderr string) string {
	t.Helper()
	m := regexp.MustCompile(`orchestration ([0-9T.]+) is running on the hub`).FindStringSubmatch(stderr)
	if m == nil {
		t.Fatalf("the command did not name the run before waiting for it:\n%s", stderr)
	}
	return m[1]
}

const gateSLS = `wait_for_go:
  salt.wait_for_event:
    - name: halite/test/go
    - timeout: 60

announce:
  salt.runner:
    - name: event.send
    - arg: [halite/test/done]
    - require:
      - salt: wait_for_go
`

const gateOnlySLS = `wait_for_go:
  salt.wait_for_event:
    - name: halite/test/go
    - timeout: 60
`

const timesoutSLS = `never:
  salt.wait_for_event:
    - name: halite/test/never
    - timeout: 3
`

type orchWaitLab struct {
	t       *testing.T
	dir     string
	hubArgs []string
	serve   []string
	running *exec.Cmd
}

// newOrchWaitLab starts a hub with an operator certificate and an
// orchestration tree, and no node: every step here runs on the hub.
func newOrchWaitLab(t *testing.T) *orchWaitLab {
	t.Helper()
	dir := t.TempDir()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	lab := &orchWaitLab{t: t, dir: dir,
		hubArgs: []string{"--config", dir + "/hub.yaml", "--root", dir, "--pki-dir", dir + "/pki"}}
	lab.write("states/gate.sls", gateSLS)
	lab.write("states/timesout.sls", timesoutSLS)
	lab.write("states/gateonly.sls", gateOnlySLS)
	lab.write("pillar/top.sls", "base: {}\n")
	lab.write("hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/state\ncache_dir: %[2]s/cache\n"+
		"file_roots: {base: [%[2]s/states]}\npillar_roots: {base: [%[2]s/pillar]}\n", addr, dir))

	// The first serve makes the CA the operator's certificate is issued
	// from; the hub reads the policy `--admin` writes when it starts, so
	// it is started again after.
	serve := append([]string{"serve", "--names", "127.0.0.1,localhost"}, lab.hubArgs...)
	first := lab.start(serve...)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(filepath.Join(dir, "pki", "ca.crt")); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = first.Process.Kill()
	_ = first.Wait()
	if got := lab.hub("keys", "operator", "create", "ed", "--admin"); got.code != 0 {
		t.Fatalf("keys operator create: %s%s", got.stdout, got.stderr)
	}
	// A second operator bound to the handbook's `deployer` role, which
	// may run orchestrations and nothing else.
	if got := lab.hub("keys", "operator", "create", "pipeline"); got.code != 0 {
		t.Fatalf("keys operator create pipeline: %s%s", got.stdout, got.stderr)
	}
	policyFile := filepath.Join(dir, "policy.yaml")
	policy, err := os.ReadFile(policyFile)
	if err != nil {
		t.Fatal(err)
	}
	withDeployer := strings.Replace(string(policy), "\nbindings:\n",
		"\n  deployer:\n    - runners: ['state.orchestrate']\n\nbindings:\n", 1) +
		"  - principal: 'cert:CN=pipeline'\n    roles: ['deployer']\n"
	if withDeployer == string(policy)+"  - principal: 'cert:CN=pipeline'\n    roles: ['deployer']\n" {
		t.Fatalf("the bootstrap policy has no bindings line to add a role before:\n%s", policy)
	}
	lab.write("policy.yaml", withDeployer)
	lab.serve = serve
	lab.startHub()
	return lab
}

// startHub starts the hub and waits until it answers an operator.
func (l *orchWaitLab) startHub() {
	l.t.Helper()
	l.running = l.start(l.serve...)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got := l.hub("runner", "manage.up"); got.code == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	l.t.Fatal("the hub never answered an operator")
}

// restartHub kills the hub, as a crash or an out-of-memory kill would,
// and starts it again on the same state.
func (l *orchWaitLab) restartHub() {
	l.t.Helper()
	_ = l.running.Process.Kill()
	_ = l.running.Wait()
	l.startHub()
}

func (l *orchWaitLab) write(rel, body string) {
	l.t.Helper()
	path := filepath.Join(l.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		l.t.Fatal(err)
	}
}

// command is this test binary run as halite-hub, with the operator
// client's timeout cut to a second. An operator command is made as `ed`,
// the administrator, unless it says otherwise: with two operator
// certificates in the directory the command will not guess.
func (l *orchWaitLab) command(args ...string) *exec.Cmd {
	operator := args[0] == "orch" || args[0] == "runner"
	if operator && !slices.Contains(args, "--as") {
		args = append(args, "--as", "ed")
	}
	cmd := exec.Command(os.Args[0], append(args, l.hubArgs...)...)
	cmd.Env = append(os.Environ(), reexec+"=1", operatorTimeoutEnv+"=1s")
	return cmd
}

func (l *orchWaitLab) start(args ...string) *exec.Cmd {
	l.t.Helper()
	cmd := l.command(args...)
	var log syncBuffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if l.t.Failed() {
			l.t.Logf("hub %v:\n%s", args[0], log.String())
		}
	})
	return cmd
}

func (l *orchWaitLab) hub(args ...string) result {
	l.t.Helper()
	cmd := l.command(args...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		l.t.Fatalf("%v: %v", args, err)
	}
	return result{out.String(), errb.String(), code}
}

// releaseAfter sends the event the gate orchestration waits for, after a
// delay, and closes the channel it returns once it has.
func (l *orchWaitLab) releaseAfter(delay time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(delay)
		if got := l.hub("runner", "event.send", "halite/test/go"); got.code != 0 {
			l.t.Errorf("event.send: %s%s", got.stdout, got.stderr)
		}
	}()
	return done
}

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
