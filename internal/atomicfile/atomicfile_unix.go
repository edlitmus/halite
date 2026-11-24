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

// Open is os.Open, for the same reason: a rename onto path after the open
// leaves the descriptor on the file that was there when it was opened.
func Open(path string) (*os.File, error) { return os.Open(path) }

// renameIn is Rename inside a root. See WriteIn.
func renameIn(root *os.Root, oldname, newname string) error { return root.Rename(oldname, newname) }
