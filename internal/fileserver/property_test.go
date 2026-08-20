package fileserver

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SPEC section 31 names "path containment never escapes a root" as a
// property. It is the CVE-2020-11652 control: Salt's file server could be
// walked out of its roots with a crafted relative path, which turned a
// read into an arbitrary file disclosure.
//
// The property is checked over generated paths rather than a fixed list,
// because the interesting inputs are the ones nobody thought to write
// down.

// pathAtoms are the fragments a traversal attempt is built from. They
// include the encodings and separators that have historically been used to
// slip past a containment check.
var pathAtoms = []string{
	"..", ".", "/", "//", "///", `\`, `\\`, "a", "sub", "..%2f", "%2e%2e",
	"....//", "..;", " ..", ".. ", "\t..", "\x00", "..\x00/", "~", "~root",
	"....", "./../", "/..", "..//..", "C:", "C:\\", "\\\\?\\", "|", ";",
	"$(pwd)", "`pwd`", "*", "?", "[a-z]", "\n..", "\r\n..", "..\\..",
}

func generatePath(rnd *rand.Rand) string {
	n := 1 + rnd.Intn(8)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = pathAtoms[rnd.Intn(len(pathAtoms))]
	}
	sep := "/"
	if rnd.Intn(4) == 0 {
		sep = ""
	}
	return strings.Join(parts, sep)
}

func TestContainmentNeverEscapesTheRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "srv", "states")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Secrets a successful escape would reach: a sibling directory, and a
	// prefix-sharing sibling that a naive strings.HasPrefix check would
	// wrongly admit.
	for _, outside := range []string{
		filepath.Join(base, "secret.txt"),
		filepath.Join(base, "srv", "states-other", "secret.txt"),
	} {
		if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "ok.sls"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}

	roots := NewRoots(map[string][]string{"base": {root}})
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	rnd := rand.New(rand.NewSource(1))
	escapes := 0
	for i := 0; i < 20000; i++ {
		rel := generatePath(rnd)
		got, err := roots.Resolve("base", rel)
		if err != nil {
			continue
		}
		// A resolution that succeeds must name a path inside the root.
		// Nothing else is acceptable, whatever the input looked like.
		if !within(realRoot, got) && got != realRoot {
			t.Errorf("%q resolved to %q, which is outside %q", rel, got, realRoot)
			if escapes++; escapes > 5 {
				t.Fatal("too many escapes to be worth listing")
			}
		}
	}
}

// A symlink inside the tree pointing out of it is the case a lexical check
// cannot see, and it is how the original CVE was exploited after the
// lexical fix.
func TestASymlinkCannotPointOutOfTheRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	links := map[string]string{
		"direct.txt": filepath.Join(outside, "secret.txt"),
		"dir":        outside,
		"relative":   "../outside",
		"deep":       "../outside/secret.txt",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}

	roots := NewRoots(map[string][]string{"base": {root}})
	roots.FollowSymlinks = true

	for _, rel := range []string{"direct.txt", "dir/secret.txt", "relative/secret.txt", "deep"} {
		got, err := roots.Resolve("base", rel)
		if err == nil {
			t.Errorf("%q resolved to %q; a symlink out of the root must be refused", rel, got)
			continue
		}
		if !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("%q was refused with %v, which does not identify the reason", rel, err)
		}
	}

	// A symlink that stays inside the root is fine, or every tree using
	// one breaks.
	if err := os.WriteFile(filepath.Join(root, "real.sls"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.sls", filepath.Join(root, "link.sls")); err != nil {
		t.Fatal(err)
	}
	if _, err := roots.Resolve("base", "link.sls"); err != nil {
		t.Errorf("a symlink inside the root was refused: %v", err)
	}
}

// Containment must hold for every entry point, not just Resolve, or the
// property is true of the function nobody calls.
func TestEveryReadPathIsContained(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots := NewRoots(map[string][]string{"base": {root}})

	rnd := rand.New(rand.NewSource(2))
	for i := 0; i < 3000; i++ {
		rel := generatePath(rnd)
		if b, _, err := roots.Read("base", rel); err == nil && string(b) == "secret" {
			t.Fatalf("Read(%q) returned the file outside the root", rel)
		}
		if b, _, err := roots.Source("base", rel); err == nil && string(b) == "secret" {
			t.Fatalf("Source(%q) returned the file outside the root", rel)
		}
		if s, _, err := roots.Templates("base").Load(rel); err == nil && s == "secret" {
			t.Fatalf("Templates().Load(%q) returned the file outside the root", rel)
		}
	}
}

// A root that is itself a symlink must not make everything inside it look
// like an escape, which is the failure mode of comparing an unresolved
// root against a resolved path.
func TestASymlinkedRootStillResolvesItsContents(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "a.sls"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	roots := NewRoots(map[string][]string{"base": {link}})
	if _, err := roots.Resolve("base", "a.sls"); err != nil {
		t.Errorf("a file inside a symlinked root was refused: %v", err)
	}
	if _, err := roots.Resolve("base", "../real/a.sls"); err == nil {
		t.Error("traversal out of a symlinked root was allowed")
	}
}
