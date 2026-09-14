package ext

// LimitSupport says which of SPEC 24.3's limits this platform can
// actually enforce, and what to call the one that bounds memory.
//
// The platforms differ in more than "yes" and "no", so a single boolean
// made the description wrong on one of them: setrlimit bounds virtual
// address space, a job object bounds committed memory, and the warning
// that belongs beside the first does not belong beside the second.
//
// Exported because the host's `Sandbox.Describe` reports it and the
// extension's Confine applies it, and the two must come from one
// declaration: a limit described as enforced and then skipped is worse
// than one nobody claimed.
type LimitSupport struct {
	// Memory, CPU, OpenFiles and Processes are whether that limit is
	// enforced at all.
	Memory    bool
	CPU       bool
	OpenFiles bool
	Processes bool
	// MemoryLabel names what the memory limit bounds, which is not the
	// same quantity on every platform that has one.
	MemoryLabel string
	// MemoryUnbounded is what to say when none is set.
	MemoryUnbounded string
}
