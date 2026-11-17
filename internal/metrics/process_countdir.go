//go:build linux || darwin

package metrics

import (
	"os"
	"strconv"
	"syscall"
)

// countDir counts this process's open descriptors from a directory that
// lists them (Linux's /proc/self/fd, macOS's /dev/fd).
//
// The listing includes whatever reading it opened, and how much that is
// belongs to Go, not to this code: on Linux one descriptor, on macOS two,
// because os.File's directory reading there dup()s the descriptor for
// fdopendir (measured on macOS 26 arm64 with Go 1.27.4: 0-2 before,
// 0-4 while listing, 0-2 after Close). A fixed "less one" was therefore
// right on one platform and wrong on the other, and found wrong only by
// TestOpenDescriptorsAgreeWithAProbe. So the listing is taken, the
// directory closed, and each number it named is asked whether it is
// still open: a descriptor the read itself held is not, and every other
// one is, whatever Go's internals do.
func countDir(dir string) (float64, bool) {
	f, err := os.Open(dir)
	if err != nil {
		return 0, false
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil || len(names) == 0 {
		return 0, false
	}
	n := 0
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0); errno == 0 {
			n++
		}
	}
	return float64(n), true
}
