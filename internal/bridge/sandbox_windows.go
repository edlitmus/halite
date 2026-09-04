package bridge

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows half of SPEC 24.3.
//
// This was the process boundary and nothing else, with a note saying so:
// "no restricted token or job object: SPEC 24.3, not built". The job
// object half is now built, and it is worth saying that it confines the
// extension more firmly than the unix side does. setrlimit cannot be
// applied to a child by its parent, so on unix the limits are handed to
// the extension in its environment and applied by the extension to
// itself — which works for one built with this package and does nothing
// for one that is not. A job object is set by the parent and enforced by
// the kernel, so it holds whatever the extension is.
//
// The restricted token is still absent, and still said so out loud. It
// is the half that would drop the extension's privileges; the job object
// bounds what it can consume, not what it is allowed to do.

// applyPlatform puts the extension in a job object carrying the
// sandbox's limits.
//
// The assignment happens after the process exists, because a job object
// cannot be assigned to one that does not. The returned cleanup closes
// the job handle: with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE set, that also
// means an extension outlives neither its timeout nor the agent.
func (s *Sandbox) applyPlatform(cmd *exec.Cmd) (func() error, func(), error) {
	nothing := func() {}

	if s.User != "" {
		// Not silently ignored. Running the extension as the agent's own
		// account when the manifest asked for a less privileged one is
		// more authority than was asked for, not less.
		return nil, nothing, fmt.Errorf(
			"this extension asks to run as %q, and starting a process as another "+
				"account on Windows needs that account's credentials, which halite "+
				"does not hold; SPEC 24.3's restricted token is not built", s.User)
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nothing, fmt.Errorf("creating a job object for the extension: %w", err)
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			// The tree dies with the job, so a killed extension does not
			// leave what it started behind, and neither does a killed
			// agent.
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if s.MemoryBytes > 0 {
		// Committed memory rather than reserved address space. That is
		// the difference that makes this safe to set on a
		// garbage-collected extension where RLIMIT_AS is not: a Go
		// runtime reserves far more address space than it commits.
		info.ProcessMemoryLimit = uintptr(s.MemoryBytes)
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
	}
	if s.Processes > 0 {
		info.BasicLimitInformation.ActiveProcessLimit = uint32(s.Processes)
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	}
	if s.CPUSeconds > 0 {
		// Per process rather than per job, to match what RLIMIT_CPU
		// means on the other side: a limit on one extension's own
		// processor time. In 100ns units, which is what the field takes.
		info.BasicLimitInformation.PerProcessUserTimeLimit = int64(s.CPUSeconds) * 10_000_000
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_TIME
	}

	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, nothing, fmt.Errorf("setting the extension's limits: %w", err)
	}

	// Its own process group as well, so a console signal aimed at the
	// agent is not delivered to the extension by the console host.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &windows.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP

	afterStart := func() error {
		if cmd.Process == nil {
			return nil
		}
		h, err := windows.OpenProcess(
			windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err != nil {
			return fmt.Errorf("confining the extension: %w", err)
		}
		defer windows.CloseHandle(h)
		if err := windows.AssignProcessToJobObject(job, h); err != nil {
			return fmt.Errorf("confining the extension: %w", err)
		}
		return nil
	}
	cleanup := func() { _ = windows.CloseHandle(job) }
	return afterStart, cleanup, nil
}

// limitsAvailable: a job object enforces memory, processor time and the
// number of processes, and the kernel does the enforcing.
//
// Not open files. There is no handle-count limit in a job object, and
// there is no counterpart to RLIMIT_NOFILE here at all; a caller that
// sets one is told it is not enforced rather than left to assume it is.
//
// The memory limit bounds committed memory rather than reserved address
// space, so the warning that belongs beside RLIMIT_AS does not belong
// here: setting one does not kill a garbage-collected extension.
func limitsAvailable() limitSupport {
	return limitSupport{
		Memory: true, CPU: true, Processes: true, OpenFiles: false,
		MemoryLabel:     "committed memory",
		MemoryUnbounded: "committed memory unbounded",
	}
}

func networkEnforcement() string {
	// The same honesty the unix side owes. A job object has no network
	// restriction; what this does is decline to say the extension may
	// use the network, which one built with this package honours.
	return "not granted; enforced only by an extension that honours the declaration"
}

func sandboxPlatformNotes() []string {
	return []string{
		"limits enforced by a job object, by the kernel rather than by the extension",
		"no restricted token: SPEC 24.3's privilege drop is not built",
	}
}
