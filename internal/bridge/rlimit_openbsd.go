//go:build openbsd

package bridge

import "syscall"

// OpenBSD has no RLIMIT_AS. It bounds memory with RLIMIT_DATA, which
// anonymous mmap counts against there and does not on Linux or the
// other BSDs — so the limit is real, but it is not the same limit: it
// bounds what a process allocates rather than what it maps. Describe
// says "data segment" rather than "address space" for exactly that
// reason, because the number an operator would pick differs.
var rlimitMemory = memoryLimit{
	resource:  syscall.RLIMIT_DATA,
	label:     "data segment",
	unbounded: "data segment unbounded (OpenBSD has no RLIMIT_AS)",
}

// RLIMIT_NPROC is 7, as on the other BSDs, and is not in `syscall`.
const rlimitProcesses = 7
