package builtin

import (
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// A file's permissions, as a state can see and set them on Windows.
//
// There is no POSIX mode here. os.Chmod sets the read-only attribute and
// nothing else; os.Stat synthesises 0666 back for anything writable. So
// `mode: '0640'` on a file state compared 0666 against 0640, found them
// different, "applied" a chmod that changed nothing, and found them
// different again on the next run. No file state on this platform ever
// converged: every run reported a change, `state.apply` never returned
// the exit code for a converged run, and a highstate could not be used
// to tell drift from noise. Three of the file conformance cases failed
// on exactly this.
//
// One part of a mode does mean something here. A mode that denies group
// and other is a statement that no other account may read the file, and
// that is expressible: an access control list granting the owner,
// SYSTEM and Administrators and nobody else. See internal/fileperm.
//
// The rest of a mode — the difference between 0644 and 0755, an execute
// bit, a setgid bit — has no counterpart. SPEC 15.3 puts it in
// `win_dacl`, which this build does not have. It is not applied, it is
// not reported as a change, and the state says so once rather than
// reporting a change forever.

// plannedMode reports whether a file's permissions already satisfy the
// requested mode, and what to record when they do not.
func plannedMode(path string, info os.FileInfo, want os.FileMode) (differs bool, change *value.Map, warning string) {
	if want.Perm()&0o077 != 0 {
		return false, nil, fmt.Sprintf(
			"the mode %s is not applied to %s: Windows has no POSIX mode, and the "+
				"permissions this build can set here are `private` or inherited. "+
				"SPEC 15.3 puts the rest in win_dacl, which this build does not have",
			formatMode(want), path)
	}
	others, err := fileperm.Others(path)
	if err != nil {
		return false, nil, fmt.Sprintf("the permissions of %s could not be read: %v", path, err)
	}
	if len(others) == 0 {
		return false, nil, ""
	}
	return true, states.Change(fmt.Sprintf("readable by %v", others), "the owner, SYSTEM and Administrators"), ""
}

// applyMode carries out the part of a mode that means something here.
func applyMode(path string, want os.FileMode) error {
	if want.Perm()&0o077 != 0 {
		// Not an error: modeDiffers has already said it will not be
		// applied, and failing the state over a mode the tree is
		// entitled to write would stop every cross-platform tree.
		return nil
	}
	return fileperm.Apply(path, want)
}
