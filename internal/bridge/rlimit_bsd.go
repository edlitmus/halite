//go:build darwin || freebsd || netbsd || dragonfly

package bridge

import "syscall"

// The BSDs spell these two differently from Linux, and neither name is
// in the portable part of `syscall`. OpenBSD is not in this file: it
// has no RLIMIT_AS at all, so it has its own.
var rlimitMemory = memoryLimit{
	resource: syscall.RLIMIT_AS,
	label:    "address space",
	unbounded: "address space unbounded " +
		"(RLIMIT_AS kills a garbage-collected runtime)",
}

// RLIMIT_NPROC is 7 on the BSDs and is not in `syscall`, which carries
// only the limits POSIX names. Written out rather than pulled in from
// golang.org/x/sys, which SPEC 4.2 makes an open question rather than a
// dependency to reach for.
const rlimitProcesses = 7
