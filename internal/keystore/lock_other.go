//go:build !unix && !windows

package keystore

import (
	"errors"
	"os"
)

// No platform halite builds for lands here (`make build-all`'s targets
// are all unix or Windows). A store that cannot exclude the CLI must not
// write as though it could, so every write is refused rather than left
// unguarded.
var errNoLock = errors.New("this platform has no file lock, and the key store will not write without one")

func lockFile(*os.File) error            { return errNoLock }
func tryLockFile(*os.File) (bool, error) { return false, errNoLock }
func unlockFile(*os.File) error          { return nil }
