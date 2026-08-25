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
		limit := syscall.Rlimit{Cur: rlimitValue(value), Max: rlimitValue(value)}
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

// rlimitValue converts to whatever width this platform's Rlimit uses:
// int64 on the BSDs, uint64 on Linux.
func rlimitValue(v uint64) rlimType { return rlimType(v) }
