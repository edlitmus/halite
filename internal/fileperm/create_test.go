package fileperm

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The three helpers, each asked the question they exist to answer: after this
// call, can another account reach it?
//
// `Others` is that question, and it is the same one `Apply` is checked with —
// from the mode on unix, from the access control list on Windows. So these
// assertions mean something on both platforms without knowing which they are
// on, which is the whole reason the helpers were worth having rather than a
// `MkdirAll` and an `Apply` repeated thirty-one times.
//
// **What they cannot see on unix**: `os.MkdirAll(dir, 0o700)` already produces a
// 0700 directory here, and `os.WriteFile(path, data, 0o600)` a 0600 file — so
// for a path each helper *creates*, these would pass against the standard
// library alone. `test (windows-2022)` is the witness for that half, as it is
// for 5.161. The one case that bites everywhere is a path that already existed,
// which is the third test.
//
// DIVERGENCE 5.164.

func private(t *testing.T, path, what string) {
	t.Helper()
	others, err := Others(path)
	if err != nil {
		t.Fatalf("who can reach %s could not be read: %v", path, err)
	}
	if len(others) > 0 {
		t.Errorf("%s (%s) can be reached by %v. %s", what, path, others, Advice(path))
	}
}

func TestMkdirAllMakesAPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store", "segments")
	if err := MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	private(t, dir, "the directory")
}

// **A directory that already existed is restricted**, which `os.MkdirAll` never
// does on any platform: it returns nil for a path that is there and changes
// nothing. That is the directory nobody goes back to check — one made by hand,
// or left by a build that predates this.
func TestMkdirAllRestrictsADirectoryThatAlreadyExisted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	// The umask may have taken bits out of 0777, so the starting condition is
	// asserted rather than assumed: a setup that did not produce the
	// condition under test makes the test pass for the wrong reason.
	if err := Apply(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if before, err := Others(dir); err != nil {
		t.Fatal(err)
	} else if len(before) == 0 {
		t.Fatalf("the setup produced a directory nobody else can reach, so this test "+
			"cannot see what it is for: %s", dir)
	}

	if err := MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	private(t, dir, "a directory that already existed")
}

func TestWriteFileMakesAPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := WriteFile(path, []byte("shhh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	private(t, path, "the file")
	// And it holds what was written, because a helper that restricted the
	// right path and wrote the wrong bytes would satisfy everything above.
	if b, err := os.ReadFile(path); err != nil || string(b) != "shhh\n" {
		t.Errorf("the file holds %q (%v)", b, err)
	}
}

// WriteFile over a file that already existed with a looser mode, which is the
// half `os.WriteFile` does not do: its mode argument applies only when the call
// creates the file.
func TestWriteFileRestrictsAFileThatAlreadyExisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("old\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := Apply(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if before, err := Others(path); err != nil {
		t.Fatal(err)
	} else if len(before) == 0 {
		t.Fatalf("the setup produced a file nobody else can reach: %s", path)
	}

	if err := WriteFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	private(t, path, "a file that already existed")
}

func TestOpenFileMakesAPrivateFileAndReturnsTheHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	f, err := OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// Private before anything is written through it, which is the reason this
	// takes the handle rather than the finished file.
	private(t, path, "the open file")
	if _, err := f.Write([]byte("line\n")); err != nil {
		t.Errorf("the handle it returned cannot be written to: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Error(err)
	}
	private(t, path, "the file after writing")
}

// A path that already existed is restricted on open, which `os.OpenFile` does
// not do: its mode applies only when the call creates the file, so a long-lived
// log reopened every start kept whatever it was created with.
func TestOpenFileRestrictsAFileThatAlreadyExisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("earlier\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := Apply(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if before, err := Others(path); err != nil {
		t.Fatal(err)
	} else if len(before) == 0 {
		t.Fatalf("the setup produced a file nobody else can reach: %s", path)
	}

	f, err := OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	private(t, path, "a log that already existed")
}

// A path that cannot be opened is an error and not a handle, and nothing is
// left behind. The directory is missing, so `os.OpenFile` fails before
// `ApplyFile` is reached -- the assertion is that the helper propagates it
// rather than returning a nil file and a nil error.
func TestOpenFileReportsAPathItCannotOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-directory", "log")
	f, err := OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		f.Close()
		t.Fatal("opening a file under a directory that does not exist succeeded")
	}
	if f != nil {
		t.Error("it returned both an error and a file")
	}
}

// **A permission somebody chose is not widened.**
//
// The regression the first version of these helpers had, and the reason it is
// worth a test of its own here rather than only in the two packages that caught
// it. `Apply` is a chmod, and a chmod widens as readily as it narrows: applying
// 0700 to a directory an operator set to 0500 makes it writable again.
//
// `internal/hub`'s `TestOpeningAnUnusableNodeCacheFailsAtOnce` and
// `internal/nodeevidence`'s `TestAnUnusableDirectoryIsRefusedAtOpen` both failed
// on it, because both make a directory 0500 and assert that opening a store
// there is refused — the store probes by writing a file, and the probe started
// succeeding. Those tests were written for "exists is not usable" (5.101) and
// they caught this because they assert on a *deliberately* tight permission,
// which nothing in this package did.
func TestMkdirAllLeavesATighterPermissionAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this test needs an unprivileged account: root writes to a directory " +
			"whatever its mode, so a widened one cannot be seen to be widened")
	}
	dir := filepath.Join(t.TempDir(), "frozen")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}

	if err := MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Still read-only, which is the assertion. Asked by trying to write,
	// because that is what a caller does and what the two tests elsewhere
	// were really checking -- and because on Windows the mode is not the
	// answer, so a mode comparison would assert nothing there.
	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o600); err == nil {
		t.Error("a directory an operator had made read-only is writable again; " +
			"MkdirAll widened a restriction somebody chose")
	}
	// And it is still private, so nothing was traded away.
	private(t, dir, "a directory left as it was")
}

// The same for a file: one at 0400 stays at 0400.
//
// WriteFile cannot be used to show it -- a 0400 file cannot be opened for
// writing at all, so the call fails before any of this is reached -- so the
// assertion is on OpenFile for reading, which is what a caller reopening an
// append log does.
func TestOpenFileLeavesATighterPermissionAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this test needs an unprivileged account: root opens a file whatever its mode")
	}
	path := filepath.Join(t.TempDir(), "sealed")
	if err := os.WriteFile(path, []byte("sealed\n"), 0o400); err != nil {
		t.Fatal(err)
	}

	f, err := OpenFile(path, os.O_RDONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Error(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o400 {
		t.Errorf("the file is %v, and was 0400; OpenFile widened it", info.Mode().Perm())
	}
	private(t, path, "a file left as it was")
}
