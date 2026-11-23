package bridge

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/edlitmus/halite/internal/winjob"
)

// Issue 608 item 1, for extensions. The extension was assigned to its
// job once it was already running, so anything it started first was
// outside the job and outside the manifest's limits. It is now created
// suspended, and this looks at it at the moment of assignment: every
// thread it has must be suspended, because a thread that is not has
// had the chance to start something.

var procSuspendThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("SuspendThread")

// suspendCounts reads the suspend count of each of a process's threads,
// by suspending each and resuming it again: SuspendThread returns the
// count it found, and x/sys has no other way to read it.
func suspendCounts(pid uint32) ([]uint32, error) {
	threads, err := winjob.Threads(pid)
	if err != nil {
		return nil, err
	}
	var counts []uint32
	for _, tid := range threads {
		h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, tid)
		if err != nil {
			return nil, err
		}
		r, _, callErr := procSuspendThread.Call(uintptr(h))
		if uint32(r) == 0xFFFFFFFF {
			windows.CloseHandle(h)
			return nil, callErr
		}
		_, err = windows.ResumeThread(h)
		windows.CloseHandle(h)
		if err != nil {
			return nil, err
		}
		counts = append(counts, uint32(r))
	}
	return counts, nil
}

func TestAnExtensionHasNotRunWhenItIsAssignedToItsJob(t *testing.T) {
	var counts []uint32
	var countErr error
	was := assignProcessToJobObject
	assignProcessToJobObject = func(job, process windows.Handle) error {
		pid, err := windows.GetProcessId(process)
		if err != nil {
			countErr = err
		} else {
			counts, countErr = suspendCounts(pid)
		}
		return was(job, process)
	}
	t.Cleanup(func() { assignProcessToJobObject = was })

	// Started under the sandbox and answering its handshake, which it
	// cannot do unless it was resumed after the assignment.
	startEcho(t, func(o *Options) {
		o.Sandbox = DefaultSandbox()
		o.Env = []string{"SystemRoot=" + systemRootForTest()}
	})

	if countErr != nil {
		t.Fatalf("reading the extension's threads at assignment: %v", countErr)
	}
	if len(counts) == 0 {
		t.Fatal("the extension had no threads at assignment, so this shows nothing")
	}
	for i, n := range counts {
		if n == 0 {
			t.Errorf("thread %d of %d was running when the extension was assigned to its job (counts %v); "+
				"anything it started was outside the job", i+1, len(counts), counts)
		}
	}
}

// An extension that could not be put in its job is refused, as it was
// before; and since it was never resumed, it never ran a line.
func TestAnExtensionThatCannotBeAssignedIsRefused(t *testing.T) {
	was := assignProcessToJobObject
	assignProcessToJobObject = func(windows.Handle, windows.Handle) error {
		return windows.ERROR_ACCESS_DENIED
	}
	t.Cleanup(func() { assignProcessToJobObject = was })

	proc, err := startEchoErr(t, func(o *Options) {
		o.Sandbox = DefaultSandbox()
		o.Env = []string{"SystemRoot=" + systemRootForTest()}
	})
	if err == nil {
		proc.Close()
		t.Fatal("an extension that could not be confined was started")
	}
	if !strings.Contains(err.Error(), "confining the extension") {
		t.Errorf("the refusal does not say what failed: %v", err)
	}
}
