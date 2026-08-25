//go:build linux

package bridge

import "syscall"

const (
	rlimitAS = syscall.RLIMIT_AS
	// RLIMIT_NPROC is 6 on Linux and is not in `syscall`, which carries
	// only the limits POSIX names. Written out rather than pulled in
	// from golang.org/x/sys, which SPEC 4.2 makes an open question
	// rather than a dependency to reach for.
	rlimitNPROC = 6
)
