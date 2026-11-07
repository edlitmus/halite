package fileperm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/fileperm/permtest"
)

// Issue 608 item 4: what a mode with group bits means on Windows.
//
// A Windows file has an owner and an access control list, and no group.
// So a mode such as 0640 makes two statements that can be carried out --
// the owner may read and write, other accounts may not -- and one that
// cannot, because there is no group to grant. The owner decided
// (2026-11-07) that the two are carried out: such a mode gives the same
// private list as 0600. Before, any group bit kept the parent's list, so
// 0640 left a file readable by every account the parent folder lets in.

func TestAGroupOnlyModeMakesAFilePrivate(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o660, 0o620} {
		path := filepath.Join(t.TempDir(), "returns.log")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		permtest.OpenToEveryone(t, path)
		if err := Apply(path, mode); err != nil {
			t.Fatalf("Apply %04o: %v", mode, err)
		}
		others, err := Others(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(others) != 0 {
			t.Errorf("after Apply %04o, the file can still be reached by %v", mode, others)
		}
	}
}

// ApplyFile is the path the file returner and the temporary scripts take.
func TestAGroupOnlyModeMakesAnOpenFilePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "returns.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	permtest.OpenToEveryone(t, path)
	if err := ApplyFile(f, 0o640); err != nil {
		t.Fatal(err)
	}
	others, err := Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 0 {
		t.Errorf("after ApplyFile 0640, the file can still be reached by %v", others)
	}
}

// A directory takes the inheriting form, as 0700 does.
func TestAGroupOnlyModeMakesADirectoryPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	permtest.OpenToEveryone(t, dir)
	if err := Apply(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	others, err := Others(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 0 {
		t.Errorf("after Apply 0750, the directory can still be reached by %v", others)
	}
	inside := filepath.Join(dir, "new")
	if err := os.WriteFile(inside, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	if others, err := Others(inside); err != nil {
		t.Fatal(err)
	} else if len(others) != 0 {
		t.Errorf("a file made inside a 0750 directory can be reached by %v", others)
	}
}

// A mode that grants other accounts something is not private, and keeps
// the list it has. That part did not change.
func TestAModeWithOtherBitsKeepsTheInheritedList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "public.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	permtest.OpenToEveryone(t, path)
	if err := Apply(path, 0o644); err != nil {
		t.Fatal(err)
	}
	others, err := Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) == 0 {
		t.Error("Apply 0644 made a file private; a mode that grants other accounts read should not")
	}
}
