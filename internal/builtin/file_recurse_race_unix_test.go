//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **A swap between the plan and the change cannot reach outside the
// tree** (issue 321, M-29). The plan found tree/owned.conf; before the
// change, its owner replaced it with a link to a file outside the tree,
// standing in for /etc/shadow. On main, os.Chmod followed the link and
// set the outside file's mode, as root would have. The swap is made here
// by hand between the two halves, which is the race made certain.
func TestARecursiveModeChangeDoesNotFollowASwappedLink(t *testing.T) {
	base := t.TempDir()
	tree := filepath.Join(base, "tree")
	outside := filepath.Join(base, "shadow")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(tree, "owned.conf")
	for _, p := range []string{owned, outside} {
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := value.MapOf("name", tree, "recurse", []any{"mode"}, "file_mode", "0644")
	f, err := parseRecurse(args)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := planRecurse(newCtx(false), tree, f, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("planned %d targets", len(targets))
	}

	if err := os.Remove(owned); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, owned); err != nil {
		t.Fatal(err)
	}
	_ = applyRecurse(tree, targets, args, f)

	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the file outside the tree became %o through the swapped link", info.Mode().Perm())
	}
}

// And the same with a parent directory swapped for a link, which
// redirected even Lchown, since Lchown protects only the last component.
func TestARecursiveModeChangeDoesNotFollowASwappedParent(t *testing.T) {
	base := t.TempDir()
	tree := filepath.Join(base, "tree")
	elsewhere := filepath.Join(base, "etc")
	for _, d := range []string{filepath.Join(tree, "sub"), elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(tree, "sub", "app.conf"), filepath.Join(elsewhere, "app.conf")} {
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := value.MapOf("name", tree, "recurse", []any{"mode"}, "file_mode", "0644", "dir_mode", "0755")
	f, err := parseRecurse(args)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := planRecurse(newCtx(false), tree, f, args)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(tree, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(tree, "sub")); err != nil {
		t.Fatal(err)
	}
	_ = applyRecurse(tree, targets, args, f)

	info, err := os.Stat(filepath.Join(elsewhere, "app.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("a file outside the tree became %o through the swapped parent", info.Mode().Perm())
	}
}
