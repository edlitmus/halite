//go:build solaris && !illumos

package keystore

import (
	"errors"
	"os"
	"syscall"
)

// Solaris's syscall package has no flock, so this is fcntl's record
// lock. It excludes other processes, which is what it is here for. It
// does not exclude another Store in the same process -- fcntl locks
// belong to the process -- and the hub and the CLI each hold one Store,
// so that matters only to a test, and the tests that rely on it run on
// the platforms that have flock. Nothing here has run on Solaris.
func lockFile(f *os.File) error {
	lk := syscall.Flock_t{Type: syscall.F_WRLCK}
	for {
		err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLKW, &lk)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func tryLockFile(f *os.File) (bool, error) {
	lk := syscall.Flock_t{Type: syscall.F_WRLCK}
	err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lk)
	if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES) {
		return false, nil
	}
	return err == nil, err
}

func unlockFile(f *os.File) error {
	lk := syscall.Flock_t{Type: syscall.F_UNLCK}
	return syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lk)
}
