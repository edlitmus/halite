package exec

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// confineTree puts the child in a job object, so that cancelling the
// command kills everything it started.
//
// This was a no-op on Windows, with a comment saying the WaitDelay set
// in Run bounded a hung child there. It bounds the wait, not the
// runaway: a `cmd.run` that timed out left every process its command had
// started still running, and on a node that runs a package manager or an
// installer that is most of the work. A job object is what this platform
// has in place of a process group, and the unix side has killed the
// group since the timeout was fixed.
//
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE means the tree also dies if halite
// itself is killed, rather than being left orphaned.
//
// The child is assigned once it has started rather than before, because
// os/exec offers no hook between CreateProcess and the first instruction
// and gives no way to resume a suspended thread. A process that forks in
// the microseconds before the assignment escapes the job; a process that
// forks after it does not, and neither did anything before this existed.
func confineTree(c *exec.Cmd) (afterStart func() error, cleanup func(), err error) {
	noAfter := func() error { return nil }
	nothing := func() {}

	job, err := createJobObject(nil, nil)
	if err != nil {
		// Without a job the command still runs; only the tree kill is
		// lost. The caller says so, once (OSRunner.confinementOff).
		return noAfter, nothing, fmt.Errorf("creating a job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return noAfter, nothing, fmt.Errorf("setting the job object's kill-on-close limit: %w", err)
	}

	// Its own process group as well, so a console signal aimed at
	// halite is not delivered to the child by the console host.
	if c.SysProcAttr == nil {
		c.SysProcAttr = &windows.SysProcAttr{}
	}
	c.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP

	c.Cancel = func() error {
		// Terminating the job takes the child and everything it
		// started. The deadline was the grace period, so this does not
		// ask first.
		_ = windows.TerminateJobObject(job, 1)
		if c.Process != nil {
			return c.Process.Kill()
		}
		return nil
	}

	afterStart = func() error {
		if c.Process == nil {
			return nil
		}
		h, err := windows.OpenProcess(
			windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(c.Process.Pid))
		if err != nil {
			return fmt.Errorf("opening the child to confine it: %w", err)
		}
		defer windows.CloseHandle(h)
		if err := assignProcessToJobObject(job, h); err != nil {
			return fmt.Errorf("assigning the child to its job object: %w", err)
		}
		return nil
	}
	cleanup = func() { _ = windows.CloseHandle(job) }
	return afterStart, cleanup, nil
}

// The two calls a test makes fail. Neither failure can be produced on a
// healthy machine on demand, and the path that handles them is the one
// that was silent.
var (
	createJobObject          = windows.CreateJobObject
	assignProcessToJobObject = windows.AssignProcessToJobObject
)
