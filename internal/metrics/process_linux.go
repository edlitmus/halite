package metrics

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// residentBytes is the second field of /proc/self/statm, which is the
// resident set in pages, the figure `ps -o rss=` prints (in KiB).
func residentBytes() (float64, bool) {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return float64(pages) * float64(os.Getpagesize()), true
}

// openFDs counts /proc/self/fd, less the descriptor the count itself
// holds open on that directory while it reads it: the figure is this
// process's descriptors as anything outside it would count them.
func openFDs() (float64, bool) { return countDir("/proc/self/fd") }

func maxFDs() (float64, bool) { return rlimitNofile() }

func rlimitNofile() (float64, bool) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0, false
	}
	return float64(lim.Cur), true
}
