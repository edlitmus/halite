package atomicfile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// WriteNew's whole claim is that the final name never exists holding less
// than all of the data. The moment that matters is the one a kill used to
// land in: after the file is created and before it is complete. linkNew is
// that moment here, so the test looks at the final name from inside it and
// then fails the link, as a kill there would.
//
// Put back the write it replaced -- open the final name with O_EXCL, then
// write -- and this fails: the name exists before the data does, and
// linkNew is never reached. DIVERGENCE 5.739.
func TestWriteNewNeverShowsAPartialName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.key")
	data := []byte("-----BEGIN PRIVATE KEY-----\nnot really\n-----END PRIVATE KEY-----\n")

	reached := false
	saved := linkNew
	t.Cleanup(func() { linkNew = saved })
	linkNew = func(oldname, newname string) error {
		reached = true
		if _, err := os.Lstat(newname); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the final name exists before it is linked: %v", err)
		}
		if got, err := os.ReadFile(oldname); err != nil || !bytes.Equal(got, data) {
			t.Errorf("the temporary file is not whole before the link: %q, %v", got, err)
		}
		return errors.New("killed here")
	}
	if err := WriteNew(path, data, 0o600); err == nil {
		t.Fatal("a failed link was reported as a write")
	}
	if !reached {
		t.Fatal("the final name was never linked: the write went somewhere else first")
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a write that never finished left the final name behind: %v", err)
	}
	assertOnly(t, dir)
}

func TestWriteNewWritesOnceAndThenRefuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.key")
	if err := WriteNew(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteNew(path, []byte("second"), 0o600)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("a second write gave %v, not fs.ErrExist", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "first" {
		t.Fatalf("the refused write changed the file: %q", got)
	}
	assertOnly(t, dir, "ca.key")
}

// The refusal is the link's, not only the check before it: the name can
// appear between the two, and a rename there would replace it.
func TestWriteNewRefusesANameThatAppearsDuringTheWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.key")
	saved := linkNew
	t.Cleanup(func() { linkNew = saved })
	linkNew = func(oldname, newname string) error {
		if err := os.WriteFile(newname, []byte("someone else's"), 0o600); err != nil {
			t.Fatal(err)
		}
		return saved(oldname, newname)
	}
	if err := WriteNew(path, []byte("mine"), 0o600); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("got %v, not fs.ErrExist", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "someone else's" {
		t.Fatalf("the name that appeared was replaced: %q", got)
	}
	assertOnly(t, dir, "node.key")
}

// assertOnly fails if dir holds anything but names: a temporary file left
// behind is a copy of a private key nobody knows about.
func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != len(names) {
		t.Fatalf("%s holds %v, want %v", dir, got, names)
	}
	for i := range got {
		if got[i] != names[i] {
			t.Fatalf("%s holds %v, want %v", dir, got, names)
		}
	}
}
