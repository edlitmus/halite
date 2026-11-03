//go:build !unix

package builtin

import (
	"os"
	"path/filepath"
)

// The mode-and-owner change of entryattrs_unix.go, where there is no
// uid, gid or link count to check. On Windows a mode is an access control
// list set by path (mode_windows.go) and an owner is win_dacl's; these
// keep the name-based changes the callers made before. What a hard link
// means there is recorded in DIVERGENCE 5.558.

type entryChange struct {
	mode     os.FileMode
	setMode  bool
	uid, gid int
	check    bool
}

func (c entryChange) setOwner() bool { return c.uid >= 0 || c.gid >= 0 }

func changeEntryIn(root *os.Root, display, rel string, c entryChange) error {
	if c.check {
		return nil
	}
	if c.setMode {
		if err := root.Chmod(rel, c.mode); err != nil {
			return err
		}
	}
	if c.setOwner() {
		return root.Lchown(rel, c.uid, c.gid)
	}
	return nil
}

func changeEntryPath(path string, c entryChange, follow bool) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return changeEntryIn(root, path, filepath.Base(path), c)
}

func skippedEntry(error) (string, bool) { return "", false }

func checkRecurseTarget(recurseTarget, string, string) error { return nil }

func ownEntryIn(root *os.Root, rel, wantUser, wantGroup string, check bool) error {
	if check {
		return nil
	}
	return applyOwnershipIn(root, rel, wantUser, wantGroup)
}
