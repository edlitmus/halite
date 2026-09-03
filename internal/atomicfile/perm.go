package atomicfile

import (
	"os"

	"github.com/edlitmus/halite/internal/fileperm"
)

// chmodTemp sets the permissions the finished file will have, before the
// rename, so that the file is never briefly reachable at its final path
// by an account that should not have it.
//
// internal/fileperm rather than os.Chmod, because a mode is not what
// decides who can read a file on every platform this ships to.
func chmodTemp(f *os.File, mode os.FileMode) error { return fileperm.ApplyFile(f, mode) }
