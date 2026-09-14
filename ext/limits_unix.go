//go:build unix

package ext

// limitsAvailable: setrlimit covers cpu and open files on every unix,
// and the child applies them to itself. The other two are read from
// this platform's own declaration rather than assumed, because not
// every unix has both — see rlimit.go.
//
// Taking them from the same constants `Confine` uses is the point: a
// limit reported as enforced and then skipped, or skipped and then
// reported, is the failure this arrangement makes impossible.
func Limits() LimitSupport {
	return LimitSupport{
		Memory:          rlimitMemory.resource != rlimitAbsent,
		CPU:             true,
		OpenFiles:       true,
		Processes:       rlimitProcesses != rlimitAbsent,
		MemoryLabel:     rlimitMemory.label,
		MemoryUnbounded: rlimitMemory.unbounded,
	}
}
