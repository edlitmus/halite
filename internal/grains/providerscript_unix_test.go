//go:build !windows

package grains

import (
	"os"
	"path/filepath"
	"testing"
)

// writeProvider writes a grain provider that emits the given JSON, in
// the form this platform runs.
func writeProvider(t *testing.T, dir, name, json string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\nprintf '%s' '" + json + "'\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeBadProvider writes one that exits non-zero having said nothing
// useful.
func writeBadProvider(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeSlowProvider writes one that never returns.
func writeSlowProvider(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
