//go:build unix

package bridge

// rlimitAbsent marks one of SPEC 24.3's limits that this platform's
// kernel does not have.
//
// Not every unix has all four. OpenBSD has no RLIMIT_AS; Solaris,
// illumos and AIX have no RLIMIT_NPROC, and bound process counts with
// resource controls, which is not a setrlimit at all. Passing a
// made-up resource number to `setrlimit` there would either fail
// quietly or bound something else, and both read to an operator as a
// limit that is in force.
//
// So a platform file says `rlimitAbsent`, `Confine` skips it, and
// `limitsAvailable` reports it unenforced — the behaviour and what
// `sys.list_extensions` tells the operator come from the same
// declaration, which is what stops the two from disagreeing.
//
// This was found by compiling for the tier 3 platforms of SPEC 27.1,
// which nothing had ever done: four of the eight did not build at all,
// and every failure was in this file's neighbours.
const rlimitAbsent = -1

// memoryLimit is the resource that bounds an extension's memory here,
// and what it actually bounds.
//
// The second field is not decoration. setrlimit's memory limit is not
// the same quantity on every platform that has one — address space on
// Linux and the BSDs, the data segment on OpenBSD — and an operator
// reading `sys.list_extensions` is entitled to know which, because the
// number that is safe for one is not safe for the other.
type memoryLimit struct {
	// resource is the setrlimit resource, or rlimitAbsent.
	resource int
	// label names what it bounds, for Describe.
	label string
	// unbounded is what Describe says when none is set.
	unbounded string
}
