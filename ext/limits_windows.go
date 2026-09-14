//go:build windows

package ext

// limitsAvailable: a job object enforces memory, processor time and the
// number of processes, and the kernel does the enforcing.
//
// Not open files. There is no handle-count limit in a job object, and
// there is no counterpart to RLIMIT_NOFILE here at all; a caller that
// sets one is told it is not enforced rather than left to assume it is.
//
// The memory limit bounds committed memory rather than reserved address
// space, so the warning that belongs beside RLIMIT_AS does not belong
// here: setting one does not kill a garbage-collected extension.
func Limits() LimitSupport {
	return LimitSupport{
		Memory: true, CPU: true, Processes: true, OpenFiles: false,
		MemoryLabel:     "committed memory",
		MemoryUnbounded: "committed memory unbounded",
	}
}
