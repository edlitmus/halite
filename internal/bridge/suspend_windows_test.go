package bridge

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/edlitmus/halite/internal/winjob"
)

// Issue 608 item 1, for extensions. The extension was assigned to its
// job once it was already running, so anything it started first was
// outside the job and outside the manifest's limits. It is now created
// suspended, assigned, and resumed, and this looks at it in between:
// once it is in its job and before it is resumed, every thread it has
// must still be suspended, because a thread that is not has had the
// chance to start something.
//
// It looked from the assignment seam at first, and failed on the
// windows-2022 runner with "Access is denied" on the fix and on the
// break alike, which showed nothing. The seam is handed the process
// handle applyPlatform opened, with PROCESS_SET_QUOTA and
// PROCESS_TERMINATE only, and the test asked GetProcessId for its ID;
// GetProcessId needs PROCESS_QUERY_INFORMATION or
// PROCESS_QUERY_LIMITED_INFORMATION on the handle. winjob.Resume, which
// opened the same threads with the same right a moment later and
// succeeded, is given the ID and never asks a handle for it. The resume
// seam is given the ID too, so the test needs no handle at all
// (DIVERGENCE 5.620.2).

var procSuspendThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("SuspendThread")

// suspendCounts reads the suspend count of each of a process's threads,
// by suspending each and resuming it again: SuspendThread returns the
// count it found, and x/sys has no other way to read it. A thread that
// exits between the snapshot and the open is left out; OpenThread says
// ERROR_INVALID_PARAMETER for an ID that names no thread.
func suspendCounts(pid uint32) ([]uint32, error) {
	threads, err := winjob.Threads(pid)
	if err != nil {
		return nil, err
	}
	var counts []uint32
	for _, tid := range threads {
		h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, tid)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("OpenThread(%d): %w", tid, err)
		}
		r, _, callErr := procSuspendThread.Call(uintptr(h))
		if uint32(r) == 0xFFFFFFFF {
			windows.CloseHandle(h)
			return nil, fmt.Errorf("SuspendThread(%d): %w", tid, callErr)
		}
		_, err = windows.ResumeThread(h)
		windows.CloseHandle(h)
		if err != nil {
			return nil, fmt.Errorf("ResumeThread(%d): %w", tid, err)
		}
		counts = append(counts, uint32(r))
	}
	return counts, nil
}

func TestAnExtensionHasNotRunWhenItIsAssignedToItsJob(t *testing.T) {
	var counts []uint32
	var countErr error
	was := resumeProcess
	resumeProcess = func(pid uint32) error {
		counts, countErr = suspendCounts(pid)
		return was(pid)
	}
	t.Cleanup(func() { resumeProcess = was })

	// Started under the sandbox and answering its handshake, which it
	// cannot do unless it was resumed after the assignment.
	startEcho(t, func(o *Options) {
		o.Sandbox = DefaultSandbox()
		o.Env = []string{"SystemRoot=" + systemRootForTest()}
	})

	if countErr != nil {
		t.Fatalf("reading the extension's threads before the resume: %v", countErr)
	}
	if len(counts) == 0 {
		t.Fatal("the extension had no threads before the resume, so this shows nothing")
	}
	for i, n := range counts {
		if n == 0 {
			t.Errorf("thread %d of %d was running before the extension was resumed (counts %v); "+
				"it ran before it was in its job, and anything it started then is outside it",
				i+1, len(counts), counts)
		}
	}
}

// The diagnosis of the first version's "Access is denied", put to the
// runner rather than taken from the documentation: a process handle
// opened with the rights applyPlatform asks for cannot be asked its
// process ID, and one with PROCESS_QUERY_LIMITED_INFORMATION added can.
func TestTheAssignmentHandleCannotBeAskedItsProcessID(t *testing.T) {
	pid := uint32(windows.GetCurrentProcessId())
	narrow, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(narrow)
	if _, err := windows.GetProcessId(narrow); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Errorf("GetProcessId on a SET_QUOTA|TERMINATE handle returned %v, want access denied; "+
			"the explanation in this file's comment is wrong", err)
	}

	wide, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
		windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(wide)
	if got, err := windows.GetProcessId(wide); err != nil || got != pid {
		t.Errorf("GetProcessId with query rights returned %d, %v; want %d", got, err, pid)
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
