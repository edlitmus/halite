//go:build !windows

package builtin

import (
	"os"
	"path/filepath"

	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// A file's permissions, as a state can see and set them.
//
// On unix the mode is the whole answer, so these are the obvious
// implementations. They exist so that the Windows file, where a mode is
// not the answer at all, has somewhere to be.

// plannedMode reports whether a file's permissions already satisfy the
// requested mode, and what to record when they do not. An empty warning
// means the platform can carry the request out.
func plannedMode(path string, info os.FileInfo, want os.FileMode) (differs bool, change *value.Map, warning string) {
	if modeBits(info.Mode()) == modeBits(want) {
		return false, nil, ""
	}
	return true, states.Change(formatMode(info.Mode()), formatMode(want)), ""
}

// applyMode sets a file's permissions to the requested mode. It follows a
// symbolic link at path, as chmod(2) does, and refuses a hard link another
// account could have planted; see entryattrs_unix.go.
func applyMode(path string, want os.FileMode) error {
	return changeEntryPath(path, entryChange{mode: want, setMode: true, uid: -1, gid: -1}, true)
}

// applyModeIn is applyMode for a name inside an os.Root, which cannot be
// led out of the root by a symbolic link, nor through a hard link in a
// directory another account can write. dir is the root's path, which the
// Windows version needs and this one uses only to name the file.
func applyModeIn(root *os.Root, dir, name string, want os.FileMode) error {
	return changeEntryIn(root, filepath.Join(dir, name), name,
		entryChange{mode: want, setMode: true, uid: -1, gid: -1})
}
