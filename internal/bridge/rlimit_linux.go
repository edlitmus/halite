//go:build linux

package bridge

import "syscall"

var rlimitMemory = memoryLimit{
	resource: syscall.RLIMIT_AS,
	label:    "address space",
	unbounded: "address space unbounded " +
		"(RLIMIT_AS kills a garbage-collected runtime)",
}

// RLIMIT_NPROC is 6 on Linux and is not in `syscall`, which carries
// only the limits POSIX names. Written out rather than pulled in from
// golang.org/x/sys, which SPEC 4.2 makes an open question rather than a
// dependency to reach for.
const rlimitProcesses = 6
