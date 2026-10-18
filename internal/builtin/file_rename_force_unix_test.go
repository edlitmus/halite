//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **file.rename with force never loses the only copy** (issue 321,
// section 5). It removed the destination before it knew the move would
// work, so a source that was the destination -- the same path, or the
// same file through a link -- was deleted, and the move then failed with
// nothing left anywhere.
func TestFileRenameForceOntoItselfKeepsTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "only")
	if err := os.WriteFile(path, []byte("the only copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "via")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{path, filepath.Join(link, "only")} {
		res := run(t, New(), "file.rename", value.MapOf("name", path, "source", source, "force", true), false)
		if !res.Succeeded() {
			t.Errorf("source %s onto itself: %s", source, res.Comment)
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != "the only copy\n" {
			t.Fatalf("after moving %s onto itself the file is %q, %v", source, b, err)
		}
	}
}

// **And a move that fails puts the old destination back.** The move here
// fails because the source's directory is read-only, so the source cannot
// be unlinked; the copy it falls back to then cannot remove the source
// either. The destination was already gone. As root the directory's mode
// does not stop anything, so it skips there.
func TestFileRenameForceThatFailsKeepsTheDestination(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root a read-only directory does not make the move fail")
	}
	srcDir := t.TempDir()
	source := filepath.Join(srcDir, "new")
	if err := os.WriteFile(source, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(srcDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(srcDir, 0o755) })
	dest := filepath.Join(t.TempDir(), "old")
	if err := os.WriteFile(dest, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := run(t, New(), "file.rename", value.MapOf("name", dest, "source", source, "force", true), false)
	if res.Succeeded() {
		t.Fatalf("the move was meant to fail: %s", res.Comment)
	}
	if b, err := os.ReadFile(dest); err != nil || string(b) != "old\n" {
		t.Errorf("after a failed move the destination is %q, %v; it was \"old\\n\"", b, err)
	}
	if b, err := os.ReadFile(source); err != nil || string(b) != "new\n" {
		t.Errorf("after a failed move the source is %q, %v", b, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Errorf("the destination's directory holds %d entries after a failed move; it held one", len(entries))
	}
}
