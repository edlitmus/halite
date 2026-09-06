package doctor

import (
	"context"
	"fmt"
)

// FIPSState is what the two halves report, gathered by the caller so
// that this check can be run for a platform it is not on.
type FIPSState struct {
	// Kernel is the host kernel's FIPS mode, or nil where the platform
	// has no such thing.
	//
	// A pointer rather than a bool because "off" and "there is no such
	// switch" are different answers and the check gives different
	// advice for them. The `fips_mode` grain reports false on the BSDs
	// and macOS so that a template reading it does not have to guard
	// for the platform — a sensible choice there and the wrong one
	// here, where the distinction is the point.
	Kernel *bool
	// Artifact is whether this binary was built against the certified
	// module: one of the `-fips` artifacts, rather than a build that
	// merely has FIPS mode switched on.
	Artifact bool
	// Enabled is whether the module is in FIPS mode in this process.
	Enabled bool
	// Module is the certified module's version, empty when this is not
	// a FIPS artifact or the toolchain cannot say.
	Module string
	// Platform is GOOS, for the message.
	Platform string
	// NoKernel is why Kernel is nil, in the caller's words.
	//
	// The caller knows and this package does not: "freebsd has no
	// kernel FIPS mode" and "this command does not read the Windows
	// policy value" are both reasons for a nil, and a message written
	// here would have to guess which. The first version guessed, and
	// told a Windows operator that Windows has no kernel FIPS mode in
	// the same breath as saying the check applies on Windows.
	NoKernel string
}

// FIPSConsistency is SPEC 27.4's mismatch warning: "The `fips_mode`
// grain reports both the host's kernel FIPS state and the binary's own
// mode, and a mismatch is a `doctor` warning."
//
// # Why this is not one boolean
//
// A FIPS-validated binary is not a FIPS system. The kernel and the
// binary together are what an assessment covers, and they are
// independent here in a way that is easy to miss: a Go binary built
// with GOFIPS140 carries its own certified module and does not use the
// kernel crypto API at all, so `/proc/sys/crypto/fips_enabled` and this
// process's own mode can disagree in either direction and neither
// notices.
//
// Both directions are worth a warning and they are different warnings:
//
//   - A `-fips` artifact on a host whose kernel is not in FIPS mode is
//     a system that reads as compliant and is not. This is the one that
//     costs an assessment, because everything on the box says FIPS
//     except the box.
//   - An ordinary build on a host that *is* in FIPS mode makes halite
//     the non-compliant component on an otherwise compliant host. The
//     fix is the other artifact set, and an operator who sees only the
//     kernel's state will not think to look.
//
// # And the case that is neither
//
// On the BSDs and macOS there is no kernel FIPS mode to be consistent
// with. Warning there would be crying wolf on every host — and this
// project's own fleet is four FreeBSD hosts to one Linux, so a check
// that warns on four of five nodes is a check nobody reads. The honest
// answer is that the question does not arise, and `Skip` says so with
// the reason. It becomes a warning only if a FIPS artifact somehow
// turns up on such a host, which the Makefile's `FIPS_TARGETS` does not
// build.
func FIPSConsistency(state FIPSState) Check {
	return Check{
		Name:  "FIPS mode consistency",
		Roles: []string{RoleNode, RoleHub},
		Run: func(context.Context) Result {
			res := Result{Name: "FIPS mode consistency"}

			if state.Kernel == nil {
				if state.Artifact {
					res.Status = Warn
					why := state.NoKernel
					if why == "" {
						why = state.Platform + " has no kernel FIPS mode"
					}
					res.Detail = fmt.Sprintf("this is a FIPS build (module %s) and %s",
						state.Module, why)
					res.Remedy = "A certified module on a host with no kernel FIPS mode is not a " +
						"FIPS system; only halite's own cryptography is covered.\n" +
						"The FIPS artifacts are built for Linux only — check how this binary " +
						"reached this host."
					return res
				}
				res.Status = Skip
				why := state.NoKernel
				if why == "" {
					why = state.Platform + " has no kernel FIPS mode"
				}
				res.Detail = why + ", and this is not a FIPS build"
				res.Remedy = "Nothing here claims FIPS, so there is nothing to be consistent " +
					"about."
				return res
			}

			kernel := *state.Kernel
			switch {
			case kernel && state.Artifact && state.Enabled:
				res.Status = Pass
				res.Detail = fmt.Sprintf(
					"the kernel is in FIPS mode and this is a FIPS build (module %s, in FIPS mode)",
					state.Module)

			case kernel && state.Artifact && !state.Enabled:
				// Built against the module and not running in FIPS
				// mode: the GODEBUG the service unit is meant to set.
				res.Status = Warn
				res.Detail = fmt.Sprintf(
					"the kernel is in FIPS mode and this FIPS build (module %s) is not running "+
						"in FIPS mode", state.Module)
				res.Remedy = "The binary carries the certified module and is not using it. " +
					"SPEC 27.4 has the service unit set GODEBUG=fips140=on.\n" +
					"Check the unit or rc.d script this process was started from."

			case kernel && !state.Artifact:
				res.Status = Warn
				res.Detail = "the kernel is in FIPS mode and this is not a FIPS build"
				res.Remedy = "halite is the non-compliant component on an otherwise compliant " +
					"host: its cryptography is the toolchain's, not the certified module's.\n" +
					"Install the `-fips` artifacts, which are built with GOFIPS140=" +
					"v1.0.0 (`make fips-cross`)."

			case !kernel && state.Artifact:
				res.Status = Warn
				res.Detail = fmt.Sprintf(
					"this is a FIPS build (module %s) and the kernel is not in FIPS mode",
					state.Module)
				res.Remedy = "This host reads as compliant and is not: halite's own cryptography " +
					"is certified and nothing else on the host is.\n" +
					"Put the kernel in FIPS mode — `fips=1` on the kernel command line, " +
					"Ubuntu Pro's FIPS kernel, or `fips-mode-setup --enable` on the RHEL " +
					"family — or install the ordinary artifacts and stop claiming FIPS."

			default:
				res.Status = Pass
				res.Detail = "neither the kernel nor this build is in FIPS mode"
			}
			return res
		},
	}
}
