//go:build !windows

package builtin

import (
	"os"
	"testing"
)

// assertMode checks a path's permissions against the mode that was
// asked for. A mode is the whole answer on unix.
func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want.Perm() {
		t.Errorf("%s is %04o, want %04o", path, info.Mode().Perm(), want.Perm())
	}
}
