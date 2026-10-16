//go:build unix && (!solaris || illumos)

package keystore

import (
	"errors"
	"os"
	"syscall"
)

// flock rather than fcntl: an flock belongs to the open file, so two
// Stores in one process exclude each other exactly as two processes do,
// and closing some other descriptor on the same file does not quietly
// drop it. Both are fcntl's documented behaviour, and either would make
// the in-process tests of this lock prove less than they appear to.
func lockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func tryLockFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
