//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package bridge

import "syscall"

// The BSDs spell these two differently from Linux, and neither name is
// in the portable part of `syscall`.
const (
	rlimitAS = syscall.RLIMIT_AS
	// RLIMIT_NPROC is 7 on the BSDs and is not in `syscall`, which
	// carries only the limits POSIX names. Written out rather than
	// pulled in from golang.org/x/sys, which SPEC 4.2 makes an open
	// question rather than a dependency to reach for.
	rlimitNPROC = 7
)
