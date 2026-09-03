//go:build !windows

package builtin

import (
	"os"

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
	if info.Mode().Perm() == want.Perm() {
		return false, nil, ""
	}
	return true, states.Change(formatMode(info.Mode()), formatMode(want)), ""
}

// applyMode sets a file's permissions to the requested mode.
func applyMode(path string, want os.FileMode) error { return os.Chmod(path, want) }
