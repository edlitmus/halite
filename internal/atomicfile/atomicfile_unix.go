//go:build !windows

package atomicfile

import "os"

// Rename is rename(2), which is atomic and does not care who has the
// destination open.
func Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// Read is os.ReadFile. A concurrent rename onto this path leaves the
// open file descriptor pointing at the old inode, so the read returns a
// whole version of the file either way.
func Read(path string) ([]byte, error) { return os.ReadFile(path) }

// chmodTemp sets the mode the finished file will have. Doing it before
// the rename rather than after means the file is never briefly readable
// by somebody it should not be.
func chmodTemp(f *os.File, mode os.FileMode) error { return f.Chmod(mode) }
