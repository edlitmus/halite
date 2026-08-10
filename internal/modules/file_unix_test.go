//go:build !windows

package modules

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFileDirectoryCreationModeSurvivesUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	name := filepath.Join(t.TempDir(), "spool")
	res := fileDirectory(&Ctx{}, name, map[string]any{"mode": "0755"})
	if !res.Ok || !res.Changed {
		t.Fatalf("creation failed: %+v", res)
	}
	st, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	// MkdirAll alone would have produced 0700 under this umask.
	if st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 0755", st.Mode().Perm())
	}
}
