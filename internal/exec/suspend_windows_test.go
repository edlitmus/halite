package exec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/edlitmus/halite/internal/winjob"
)

// Issue 608 item 1, the fix.
//
// The job object was assigned once the child was already running, so a
// grandchild the child started first was outside the job and outlived
// the timeout. TestTimeoutKillsAGrandchildStartedBeforeTheChildsMain
// could not make that happen on a runner (DIVERGENCE 5.620): a Go child
// takes milliseconds to reach its own code, and the gap was a few system
// calls. So this test does not rely on winning the race. It widens the
// gap on purpose, by making the assignment wait a second, and asks what
// the child did in that second. On the old order it ran, started its
// grandchild outside the job, and the grandchild survived the timeout.
// Created suspended, it did nothing until it was in the job, so the
// grandchild is born inside it and dies with it.

// spawnReportEnv names the file the child writes its grandchild's
// process ID to. From the environment, because init runs before the
// testing package parses its flags.
const spawnReportEnv = "HALITE_TEST_SPAWN_REPORT"

func init() {
	pidFile := os.Getenv(spawnReportEnv)
	if pidFile == "" {
		return
	}
	os.Unsetenv(spawnReportEnv)
	argv := helperArgv("sleep", "60s")
	c := exec.Command(argv[0], argv[1:]...)
	c.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Written to a temporary name and renamed, so the test never reads
	// half a number.
	tmp := pidFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(c.Process.Pid)), 0o600); err != nil {
		os.Exit(1)
	}
	if err := os.Rename(tmp, pidFile); err != nil {
		os.Exit(1)
	}
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

func TestAGrandchildStartedBeforeTheAssignmentIsInTheJobAndDiesWithIt(t *testing.T) {
	const assignDelay = time.Second

	jobs := make(chan windows.Handle, 1)
	was := assignProcessToJobObject
	assignProcessToJobObject = func(j, p windows.Handle) error {
		// A handle of the test's own, so the job can still be asked what
		// it holds while the command runs. It does not keep the tree
		// alive: the timeout terminates the job rather than closing it.
		var mine windows.Handle
		self := windows.CurrentProcess()
		if err := windows.DuplicateHandle(self, j, self, &mine, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return err
		}
		jobs <- mine
		time.Sleep(assignDelay)
		return was(j, p)
	}
	t.Cleanup(func() { assignProcessToJobObject = was })

	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := helperCommand("sleep", "60s")
	cmd.Env = append(CleanEnv(), spawnReportEnv+"="+pidFile)
	cmd.Timeout = 8 * time.Second
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		_, err := (&OSRunner{}).Run(context.Background(), cmd)
		done <- err
		close(finished)
	}()
	// Before the seam is put back, on every path out of the test, so
	// that a failure here is not also reported as a data race.
	t.Cleanup(func() { <-finished })

	var job windows.Handle
	select {
	case job = <-jobs:
		t.Cleanup(func() { _ = windows.CloseHandle(job) })
	case err := <-done:
		t.Fatalf("the command ended (%v) without reaching the job assignment", err)
	}
	grandchild := waitForPID(t, pidFile, cmd.Timeout)
	t.Cleanup(func() { killPID(grandchild) })

	ids, err := winjob.ProcessIDs(job)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids, grandchild) {
		t.Errorf("the job holds %v, without the grandchild %d the child started on its first line; "+
			"the child ran before it was assigned", ids, grandchild)
	}

	if err := <-done; err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("the command was meant to time out and returned %v", err)
	}
	if alive(grandchild, 5*time.Second) {
		t.Errorf("the grandchild %d outlived the timeout's kill of the job", grandchild)
	}
}

// waitForPID waits for the child to say which process it started.
func waitForPID(t *testing.T, path string, within time.Duration) uint32 {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 32)
			if err != nil {
				t.Fatalf("the child wrote %q as its grandchild's ID", b)
			}
			return uint32(n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the child did not report a grandchild within %s, so this shows nothing", within)
	return 0
}

// alive reports whether a process is still running after waiting up to
// d for it to end. One that cannot be opened has ended and been reaped.
func alive(pid uint32, d time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, uint32(d.Milliseconds()))
	return ev == uint32(windows.WAIT_TIMEOUT)
}

func killPID(pid uint32) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.TerminateProcess(h, 1)
}
