package metrics

import "syscall"

// residentBytes is not read on macOS. The kernel gives a process's
// resident size through task_info or proc_pidinfo, neither of which
// this build can call without cgo, and macOS is not a production
// platform here (CLAUDE.md: four FreeBSD hosts to one Ubuntu). So the
// family is absent rather than approximated.
func residentBytes() (float64, bool) { return 0, false }

// openFDs counts /dev/fd, which on macOS lists every descriptor the
// process has open, not only 0, 1 and 2.
func openFDs() (float64, bool) { return countDir("/dev/fd") }

func maxFDs() (float64, bool) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0, false
	}
	return float64(lim.Cur), true
}
