//go:build aix || solaris || illumos

package bridge

import "syscall"

// RLIMIT_AS is 6 here and `syscall` does carry it, which is the one
// part of this that needs no explaining.
var rlimitMemory = memoryLimit{
	resource: syscall.RLIMIT_AS,
	label:    "address space",
	unbounded: "address space unbounded " +
		"(RLIMIT_AS kills a garbage-collected runtime)",
}

// There is no RLIMIT_NPROC to set. Solaris and illumos bound the number
// of processes with resource controls — project.max-lwps and the zone's
// equivalent — which are set by the operator on the project or zone and
// not by a process on itself, so there is nothing `Confine` can do. AIX
// is grouped here because `syscall` carries no RLIMIT_NPROC for it
// either, and declaring a limit this build cannot verify would be worse
// than declaring none: `Describe` says it is not enforced, which is
// true, rather than naming a number nobody has watched take effect.
const rlimitProcesses = rlimitAbsent
