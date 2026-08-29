//go:build unix

package bridge

import (
	"os"
	"strconv"
	"syscall"
)

// Confine applies the resource limits the host asked for.
//
// Called by the extension on itself, because `setrlimit` applies to the
// calling process: the host cannot set a child's limits without
// setting its own, and Go's `SysProcAttr` has no field for them. So the
// host names them in the environment and an extension built with this
// package applies them here.
//
// The honest consequence, which `Sandbox.Describe` states: an extension
// that is not built with this package ignores them. The process
// boundary and the dropped identity are enforced by the host and hold
// for any extension; the limits hold for a cooperating one.
func Confine() {
	apply := func(name string, resource int) {
		raw := os.Getenv(name)
		if raw == "" {
			return
		}
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return
		}
		var limit syscall.Rlimit
		setRlimit(&limit.Cur, value)
		setRlimit(&limit.Max, value)
		// A failure is ignored on purpose. An unprivileged process
		// cannot raise a limit, and refusing to start because the host
		// asked for a bound this process already has is worse than
		// running under the tighter one.
		_ = syscall.Setrlimit(resource, &limit)
	}
	apply("HALITE_EXT_RLIMIT_AS", rlimitAS)
	apply("HALITE_EXT_RLIMIT_CPU", syscall.RLIMIT_CPU)
	apply("HALITE_EXT_RLIMIT_NOFILE", syscall.RLIMIT_NOFILE)
	apply("HALITE_EXT_RLIMIT_NPROC", rlimitNPROC)
}

// NetworkDenied reports whether the host declined to grant the network.
//
// An extension built with this package checks it and does not dial. It
// is a declaration honoured rather than a boundary enforced, which is
// the difference `Sandbox.Describe` spells out.
func NetworkDenied() bool { return os.Getenv("HALITE_EXT_NETWORK") == "deny" }

// setRlimit writes a limit into a syscall.Rlimit field of whatever width
// the platform gave it.
//
// The width is not the same everywhere — int64 on FreeBSD and NetBSD,
// uint64 on Linux, macOS, and OpenBSD — and it used to be declared by
// build tag. That grouped macOS with the BSDs, which is right about
// nearly everything and wrong about this, so the tree did not compile
// there at all. Taking the field by pointer lets the compiler supply the
// type, and there is nothing left to get wrong.
func setRlimit[T int64 | uint64](field *T, v uint64) { *field = T(v) }
