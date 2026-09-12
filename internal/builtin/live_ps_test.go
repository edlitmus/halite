package builtin

import (
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `ps`, against the real process table on the machine running the
// tests.
//
// The whole module is exercised here, mutating half included, and it
// needs no privilege: every process this signals is one this test
// started. That is the rule rather than a convenience -- a test that
// pattern-matches the process table of a machine somebody is using can
// kill something they are using -- so each child is marked with a
// string that exists nowhere else on the host, and every pattern in
// this file is anchored to that marker.

// psTestContext is a context with nothing configured, which is what a
// read of the process table needs.
func psTestContext() *hexec.Context { return &hexec.Context{} }

func psCall(t *testing.T, fn string, kv ...any) any {
	t.Helper()
	args := value.NewMap(len(kv) / 2)
	for i := 0; i+1 < len(kv); i += 2 {
		args.Set(kv[i].(string), kv[i+1])
	}
	out, err := New().Exec.Call(psTestContext(), fn, args)
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return out
}

func psSkipUnlessUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("ps is a unix program and this module declares no Windows support")
	}
	if (&hexec.Context{}).Which("ps") == "" {
		t.Skip("this host has no ps")
	}
}

// marker is a string that will not be in any other command line on the
// machine, so a pattern built from it can only match this test's own
// children.
func psMarker(t *testing.T) string {
	t.Helper()
	return "halite-ps-test-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// startMarkedChild runs a shell that sleeps with the marker in its
// argument list, and returns its pid. It is killed at the end of the
// test whatever happened, so a failing assertion does not leave a
// process behind.
func startMarkedChild(t *testing.T, marker string) int {
	t.Helper()
	// A shell blocked on a builtin, rather than one running `sleep`.
	//
	// `sh -c "sleep 300 # marker"` looks like the obvious way to put a
	// marker in the process table and does not work: a shell `exec`s
	// the last command of a `-c` string, so the process that remains is
	// `sleep 300` and the marker is gone with the shell that held it.
	// That cost the first run of this file two failures. `read` is a
	// builtin, so there is nothing to exec into: the shell stays,
	// blocked on a pipe this test holds open, with the marker in its
	// own command line and no grandchild to leak.
	cmd := exec.Command("/bin/sh", "-c", "read line # "+marker)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("opening the child's stdin: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a child: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// The process table is not updated synchronously with fork on every
	// platform, so the child is waited for rather than assumed.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		procs, err := psList(psTestContext())
		if err != nil {
			t.Fatalf("reading the process table: %v", err)
		}
		for _, p := range procs {
			if p.PID == int64(cmd.Process.Pid) {
				return cmd.Process.Pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the child %d never appeared in the process table", cmd.Process.Pid)
	return 0
}

func TestLivePSReadsTheRealProcessTable(t *testing.T) {
	psSkipUnlessUnix(t)

	pids, ok := psCall(t, "ps.pid_list").([]any)
	if !ok || len(pids) == 0 {
		t.Fatalf("ps.pid_list answered %#v", pids)
	}

	// This process is in its own process table, which is the cheapest
	// assertion that the reader read something real rather than parsed
	// an empty answer into an empty list.
	self := int64(os.Getpid())
	found := false
	for _, raw := range pids {
		if n, ok := raw.(int64); ok && n == self {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ps.pid_list has %d entries and none of them is this process (%d)", len(pids), self)
	}

	info, ok := psCall(t, "ps.proc_info", "pid", self).(*value.Map)
	if !ok {
		t.Fatalf("ps.proc_info answered a %T", info)
	}
	for _, field := range []string{"pid", "ppid", "user", "name", "cpu_percent", "mem_percent",
		"rss_kb", "vsz_kb", "state", "command"} {
		if _, has := info.Get(field); !has {
			t.Errorf("ps.proc_info has no %q: %v", field, info.StringKeys())
		}
	}
	if got, _ := info.Get("pid"); got != self {
		t.Errorf("ps.proc_info returned pid %v for %d", got, self)
	}
	// The test binary's own resident size is not zero on any machine
	// that can run it, and a parser that dropped a column would report
	// one of these as zero rather than failing.
	if rss, _ := info.Get("rss_kb"); rss == int64(0) {
		t.Errorf("this process reports %v KiB resident, which is not a number a running process has", rss)
	}
	if command, _ := info.Get("command"); !strings.Contains(value.KeyString(command), "builtin") {
		t.Errorf("this process's command reads %q and should name the test binary", command)
	}
}

// A process that is not there is a refusal, not an empty answer. A tree
// asking about a pid it has just read from somewhere else needs to be
// able to tell "gone" from "here with nothing to say".
func TestLivePSRefusesAProcessThatIsNotThere(t *testing.T) {
	psSkipUnlessUnix(t)

	// A pid above every platform's default maximum, so it is absent
	// rather than somebody else's.
	_, err := New().Exec.Call(psTestContext(), "ps.proc_info", value.MapOf("pid", int64(4194303)))
	if err == nil {
		t.Fatal("ps.proc_info answered for a process that does not exist")
	}
	if !strings.Contains(err.Error(), "4194303") {
		t.Errorf("the refusal does not name the process: %v", err)
	}
}

func TestLivePSFindsAProcessByPatternAndByAccount(t *testing.T) {
	psSkipUnlessUnix(t)
	marker := psMarker(t)
	pid := startMarkedChild(t, marker)

	// The marker is in the command line and not in the program name, so
	// this is the case `full` exists for.
	matched, ok := psCall(t, "ps.pgrep", "pattern", marker, "full", true).([]any)
	if !ok || len(matched) == 0 {
		t.Fatalf("ps.pgrep did not find the child: %#v", matched)
	}
	if !psContainsPID(matched, pid) {
		t.Errorf("ps.pgrep found %v, which does not include %d", matched, pid)
	}

	// Without `full` it matches the program name, which is the shell
	// rather than the marker, so the marker finds nothing.
	if got, _ := psCall(t, "ps.pgrep", "pattern", marker).([]any); len(got) != 0 {
		t.Errorf("ps.pgrep matched %v against the program name, which does not hold the marker", got)
	}

	// Restricted to this account, which the child belongs to.
	u, err := user.Current()
	if err != nil {
		t.Skipf("this account has no name to filter on: %v", err)
	}
	me := u.Username
	mine, _ := psCall(t, "ps.pgrep", "pattern", marker, "full", true, "user", me).([]any)
	if !psContainsPID(mine, pid) {
		t.Errorf("ps.pgrep for user %q found %v, which does not include %d", me, mine, pid)
	}
	other, _ := psCall(t, "ps.pgrep", "pattern", marker, "full", true,
		"user", "nobody-who-owns-nothing").([]any)
	if len(other) != 0 {
		t.Errorf("ps.pgrep for an account that owns nothing found %v", other)
	}

	// psaux answers with the rows rather than the numbers.
	rows, ok := psCall(t, "ps.psaux", "pattern", marker).([]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("ps.psaux did not find the child: %#v", rows)
	}
	row, ok := rows[0].(*value.Map)
	if !ok {
		t.Fatalf("ps.psaux returned a %T", rows[0])
	}
	if command, _ := row.Get("command"); !strings.Contains(value.KeyString(command), marker) {
		t.Errorf("the row's command is %q and does not hold the marker", command)
	}
}

func TestLivePSTopAnswersInOrder(t *testing.T) {
	psSkipUnlessUnix(t)

	for _, by := range []string{"cpu", "memory"} {
		rows, ok := psCall(t, "ps.top", "num_processes", int64(3), "by", by).([]any)
		if !ok {
			t.Fatalf("ps.top by %s answered a %T", by, rows)
		}
		if len(rows) == 0 || len(rows) > 3 {
			t.Fatalf("ps.top by %s returned %d rows, want between 1 and 3", by, len(rows))
		}
		field := "cpu_percent"
		if by == "memory" {
			field = "rss_kb"
		}
		previous := -1.0
		for i, raw := range rows {
			row, ok := raw.(*value.Map)
			if !ok {
				t.Fatalf("row %d is a %T", i, raw)
			}
			got, _ := row.Get(field)
			current := psNumber(got)
			if previous >= 0 && current > previous {
				t.Errorf("ps.top by %s returned %v after %v, which is out of order", by, current, previous)
			}
			previous = current
		}
	}

	if _, err := New().Exec.Call(psTestContext(), "ps.top", value.MapOf("by", "colour")); err == nil {
		t.Error("ps.top accepted an ordering it does not have")
	}
}

// The mutating half, against processes this test started and nothing
// else.
func TestLivePSSignalsOnlyWhatThisTestStarted(t *testing.T) {
	psSkipUnlessUnix(t)
	marker := psMarker(t)
	pid := startMarkedChild(t, marker)

	// Test mode changes nothing, which is the contract every mutating
	// function here is held to.
	testCtx := &hexec.Context{Test: true}
	if _, err := New().Exec.Call(testCtx, "ps.kill_pid", value.MapOf("pid", int64(pid))); err != nil {
		t.Fatalf("ps.kill_pid in test mode: %v", err)
	}
	if !psAlive(t, pid) {
		t.Fatal("ps.kill_pid in test mode killed the process")
	}

	if _, err := New().Exec.Call(psTestContext(), "ps.kill_pid",
		value.MapOf("pid", int64(pid), "signal", "KILL")); err != nil {
		t.Fatalf("ps.kill_pid: %v", err)
	}
	psWaitGone(t, pid)

	// pkill, against a second child and its own marker.
	second := psMarker(t)
	secondPID := startMarkedChild(t, second)
	killed, err := New().Exec.Call(psTestContext(), "ps.pkill",
		value.MapOf("pattern", second, "full", true, "signal", "KILL"))
	if err != nil {
		t.Fatalf("ps.pkill: %v", err)
	}
	m, ok := killed.(*value.Map)
	if !ok || m.Len() == 0 {
		t.Fatalf("ps.pkill answered %#v", killed)
	}
	if _, has := m.Get(strconv.Itoa(secondPID)); !has {
		t.Errorf("ps.pkill reported %v and not %d", m.StringKeys(), secondPID)
	}
	psWaitGone(t, secondPID)
}

// A pattern that matches nothing is a refusal. It is far more often a
// misspelling than a tidy machine, and reporting success for it is how
// a tree comes to believe it has stopped something it never named.
func TestLivePSPkillRefusesAPatternThatMatchesNothing(t *testing.T) {
	psSkipUnlessUnix(t)

	_, err := New().Exec.Call(psTestContext(), "ps.pkill",
		value.MapOf("pattern", psMarker(t)+"-matches-nothing", "full", true))
	if err == nil {
		t.Fatal("ps.pkill reported success for a pattern matching no process")
	}
	if !strings.Contains(err.Error(), "no process matches") {
		t.Errorf("the refusal reads %q", err)
	}
}

// ---- helpers ----

func psContainsPID(list []any, pid int) bool {
	for _, raw := range list {
		if n, ok := raw.(int64); ok && n == int64(pid) {
			return true
		}
	}
	return false
}

func psNumber(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return 0
}

func psAlive(t *testing.T, pid int) bool {
	t.Helper()
	procs, err := psList(psTestContext())
	if err != nil {
		t.Fatalf("reading the process table: %v", err)
	}
	for _, p := range procs {
		if p.PID == int64(pid) {
			return true
		}
	}
	return false
}

// psWaitGone waits for a signalled process to leave the table. A signal
// is delivered asynchronously, and a zombie lingers until its parent
// reaps it, so this waits for absence rather than asserting it
// immediately.
func psWaitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		procs, err := psList(psTestContext())
		if err != nil {
			t.Fatalf("reading the process table: %v", err)
		}
		gone := true
		for _, p := range procs {
			if p.PID != int64(pid) {
				continue
			}
			// A zombie is gone for this purpose: it has been killed and
			// is waiting to be reaped by the test's own cleanup.
			if !strings.HasPrefix(p.State, "Z") {
				gone = false
			}
		}
		if gone {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d was signalled and is still running", pid)
}
