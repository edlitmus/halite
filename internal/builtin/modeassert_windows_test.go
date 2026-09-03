package builtin

import (
	"os"
	"testing"

	"github.com/edlitmus/halite/internal/fileperm"
)

// assertMode checks a path's permissions against the mode that was
// asked for.
//
// A mode is not the answer here. os.Stat synthesises 0666 for anything
// writable, so a test asserting `info.Mode().Perm() == 0640` read 0666
// and reported a failure that said nothing about the file. What this
// platform can be held to is the part of a mode that means something:
// a mode denying group and other says no other account may read the
// file, and that is checkable. The rest — the difference between 0644
// and 0755 — has no counterpart until win_dacl ships, and there is
// nothing to assert about it.
func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if want.Perm()&0o077 != 0 {
		return
	}
	others, err := fileperm.Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 0 {
		t.Errorf("%s was asked for mode %04o, which is private, but can be read by %v",
			path, want.Perm(), others)
	}
}
