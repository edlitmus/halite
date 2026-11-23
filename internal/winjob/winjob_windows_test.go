//go:build windows

package winjob

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// x/sys has ResumeThread and not SuspendThread. Suspending a thread and
// resuming it again is how a test reads its suspend count: SuspendThread
// returns the count it found.
var procSuspendThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("SuspendThread")

// suspendCount reads a thread's suspend count without changing it.
func suspendCount(t *testing.T, tid uint32) uint32 {
	t.Helper()
	h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, tid)
	if err != nil {
		t.Fatalf("opening thread %d: %v", tid, err)
	}
	defer windows.CloseHandle(h)
	r, _, err := procSuspendThread.Call(uintptr(h))
	if uint32(r) == 0xFFFFFFFF {
		t.Fatalf("suspending thread %d: %v", tid, err)
	}
	if _, err := windows.ResumeThread(h); err != nil {
		t.Fatalf("resuming thread %d again: %v", tid, err)
	}
	return uint32(r)
}

func newJob(t *testing.T) windows.Handle {
	t.Helper()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
	})
	return job
}

func cmdExe(t *testing.T) string {
	t.Helper()
	root := os.Getenv("SystemRoot")
	if root == "" {
		t.Fatal("SystemRoot is not set, so cmd.exe cannot be found")
	}
	return filepath.Join(root, "System32", "cmd.exe")
}

// The whole sequence the two callers use, on the real API: a process
// created suspended has one thread and it is suspended; it runs nothing
// while it waits, so the child it starts on its first line is not
// started; and once it is in a job and resumed, that child is born in
// the job.
//
// cmd.exe stands in for any program, and ping for the child it starts
// at once. Neither is a Go program, so this is not the milliseconds of
// Go runtime start-up that hid the gap on the old order (5.620).
func TestASuspendedProcessRunsNothingUntilResumedAndItsChildIsBornInTheJob(t *testing.T) {
	c := exec.Command(cmdExe(t), "/c", "ping -n 30 127.0.0.1 >NUL")
	c.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	pid := uint32(c.Process.Pid)

	threads, err := Threads(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 {
		t.Fatalf("a process created suspended has %d threads, want the one primary thread: %v", len(threads), threads)
	}
	if n := suspendCount(t, threads[0]); n < 1 {
		t.Fatalf("the primary thread's suspend count is %d; CREATE_SUSPENDED did not hold it", n)
	}

	job := newJob(t)
	// Long enough that cmd.exe, if it were running, would have started
	// ping; a race that is lost is lost in milliseconds.
	time.Sleep(time.Second)
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		t.Fatal(err)
	}
	if ids, err := ProcessIDs(job); err != nil || !slices.Equal(ids, []uint32{pid}) {
		t.Fatalf("before the resume the job holds %v (%v), want only cmd.exe %d", ids, err, pid)
	}

	if err := Resume(pid); err != nil {
		t.Fatal(err)
	}
	if n := suspendCount(t, threads[0]); n != 0 {
		t.Errorf("after Resume the primary thread's suspend count is %d, want 0", n)
	}

	// By name, because a console host the system starts for cmd.exe
	// may be in the job too, and counting would take it for ping.
	deadline := time.Now().Add(15 * time.Second)
	var names []string
	for time.Now().Before(deadline) {
		ids, err := ProcessIDs(job)
		if err != nil {
			t.Fatal(err)
		}
		names = names[:0]
		for _, id := range ids {
			names = append(names, imageName(id))
		}
		if slices.Contains(names, "ping.exe") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !slices.Contains(names, "ping.exe") {
		t.Fatalf("the job holds %v after the resume; ping, started by cmd.exe at once, was not born in it", names)
	}
	if !slices.Contains(names, "cmd.exe") {
		t.Errorf("the job holds %v, without cmd.exe itself", names)
	}
}

// imageName is the lower-cased base name of a process's program, or a
// placeholder naming the ID when it cannot be read.
func imageName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "unreadable-" + strconv.Itoa(int(pid))
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "unreadable-" + strconv.Itoa(int(pid))
	}
	return strings.ToLower(filepath.Base(windows.UTF16ToString(buf[:n])))
}

// A process that has no thread to resume is an error, never a quiet
// success: a caller told a suspended process is running waits on it to
// its timeout. Process IDs are multiples of four, so 3 is never one.
func TestResumingAProcessWithNoThreadsIsAnError(t *testing.T) {
	err := Resume(3)
	if err == nil {
		t.Fatal("Resume of a process with no threads reported success")
	}
	if !strings.Contains(err.Error(), "no threads") {
		t.Errorf("the error does not say why: %v", err)
	}
}

// A thread that has exited between the snapshot and the open is skipped,
// not fatal, as long as one listed thread was resumed. On the runner,
// with CREATE_SUSPENDED taken out on purpose, OpenThread on such a
// thread said "The parameter is incorrect" and every such command was
// killed (DIVERGENCE 5.620.2).
//
// An exited thread cannot be produced on demand at the instant between
// the two calls, so this stands one in with an ID that names no thread:
// thread IDs are multiples of four too, so 3 is never one, and OpenThread
// answers ERROR_INVALID_PARAMETER for it, as it did for the exited ones.
// That OpenThread gives this answer is checked first rather than assumed.
func TestAThreadThatIsGoneIsSkippedWhenAnotherIsResumed(t *testing.T) {
	const noSuchThread = 3
	if _, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, noSuchThread); !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		t.Fatalf("OpenThread on an ID that names no thread returned %v, not ERROR_INVALID_PARAMETER; "+
			"the stand-in is not standing in for an exited thread", err)
	}

	// Long-lived, so the real thread is still there to be read after the
	// resume.
	c := exec.Command(cmdExe(t), "/c", "ping -n 30 127.0.0.1 >NUL")
	c.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	threads, err := Threads(uint32(c.Process.Pid))
	if err != nil || len(threads) != 1 {
		t.Fatalf("the suspended process has threads %v (%v), want one", threads, err)
	}

	if err := resumeThreads([]uint32{noSuchThread, threads[0]}); err != nil {
		t.Fatalf("a gone thread beside a real one failed the resume: %v", err)
	}
	if n := suspendCount(t, threads[0]); n != 0 {
		t.Errorf("the real thread's suspend count is %d after the resume, want 0", n)
	}

	if err := resumeThreads([]uint32{noSuchThread}); err == nil || !strings.Contains(err.Error(), "no threads") {
		t.Errorf("only gone threads should be an error naming that, got %v", err)
	}
}
