package repotree

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// Walk enters this project's source and nothing else.
//
// Built as a tree rather than run against the repository, so the assertion is
// about the rule and not about what happens to be checked in today.
func TestWalkSkipsWhatIsNotThisProjectsSource(t *testing.T) {
	root := t.TempDir()
	write := func(p string) {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"internal/pkg/a.go",
		"cmd/tool/main.go",
		"vendor/golang.org/x/sys/unix/z.go",
		"bin/thing.go",
		"dist/thing.go",
		"internal/pkg/testdata/broken.go",
		"contrib/docker/x.go",
		".git/hooks/x.go",
		".claude/worktrees/other/internal/pkg/a.go",
	} {
		write(p)
	}
	// The worktree's marker, which is what makes it another checkout.
	if err := os.WriteFile(filepath.Join(root, ".claude/worktrees/other/.git"),
		[]byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var seen []string
	err := Walk(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		seen = append(seen, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(seen)

	want := []string{"cmd/tool/main.go", "internal/pkg/a.go"}
	if strings.Join(seen, " ") != strings.Join(want, " ") {
		t.Errorf("Walk saw %v, want %v", seen, want)
	}
}

// A walk error reaches the caller rather than being swallowed, so an audit that
// cannot read the tree fails instead of reporting nothing.
func TestWalkPassesAnErrorToTheCaller(t *testing.T) {
	saw := false
	err := Walk(filepath.Join(t.TempDir(), "no-such-directory"),
		func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				saw = true
			}
			return err
		})
	if err == nil {
		t.Error("walking a directory that does not exist returned no error")
	}
	if !saw {
		t.Error("the callback was not told about the error")
	}
}

// Root finds the directory holding go.mod, from wherever a test runs.
func TestRootFindsTheModuleRoot(t *testing.T) {
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Errorf("Root returned %s, which holds no go.mod: %v", root, err)
	}
	// And it is this repository rather than some enclosing module.
	if _, err := os.Stat(filepath.Join(root, "SPEC.md")); err != nil {
		t.Errorf("Root returned %s, which holds no SPEC.md: %v", root, err)
	}
}
