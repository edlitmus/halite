//go:build unix

package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

// prepareWorkDir makes dir and its parent traversable, and dir owned by
// name, as an operator would have to by hand. See its caller.
func prepareWorkDir(t *testing.T, dir, name string) {
	t.Helper()
	u, err := user.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Dir(filepath.Dir(dir)), filepath.Dir(dir)} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	t.Logf("made %s, owned by %s", dir, name)
}
