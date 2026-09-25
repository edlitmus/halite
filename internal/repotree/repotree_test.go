package repotree

import (
	"os"
	"path/filepath"
	"testing"
)

// A worktree carries a `.git` file and a nested clone a `.git` directory;
// both are another checkout. The root is not, and neither is an ordinary
// directory.
func TestOtherCheckout(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) string {
		d := filepath.Join(root, p)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := mk(".claude/worktrees/x")
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clone := mk("scratch/clone")
	if err := os.Mkdir(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := mk("internal/pkg")

	for dir, want := range map[string]bool{root: false, worktree: true, clone: true, plain: false} {
		if got := OtherCheckout(root, dir); got != want {
			t.Errorf("OtherCheckout(%s) = %v, want %v", dir, got, want)
		}
	}
}
