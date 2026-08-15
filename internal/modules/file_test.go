package modules

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAtomicWriteReplacesWithoutLeftovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := atomicWrite(path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "new\n" {
		t.Errorf("content = %q, want %q", b, "new\n")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o, want 0600", st.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want just the file: %v", len(entries), entries)
	}
}

func TestFileManagedAppliesTightenedModeWithContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are unix-only")
	}
	name := filepath.Join(t.TempDir(), "secrets.conf")
	if err := os.WriteFile(name, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := fileManaged(&Ctx{}, name, map[string]any{"contents": "new", "mode": "0600"})
	if !res.Ok || !res.Changed {
		t.Fatalf("update failed: %+v", res)
	}
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "new\n" {
		t.Errorf("content = %q", b)
	}
	st, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", st.Mode().Perm())
	}
}

func TestFileManagedKeepsExistingModeWhenUnspecified(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are unix-only")
	}
	name := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(name, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := fileManaged(&Ctx{}, name, map[string]any{"contents": "new"})
	if !res.Ok || !res.Changed {
		t.Fatalf("update failed: %+v", res)
	}
	st, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	// The write replaces the file; that must not reset a mode nobody asked
	// to change.
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want the original 0600", st.Mode().Perm())
	}
}

func TestFileDirectoryEnforcesModeOnExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory modes are unix-only")
	}
	name := filepath.Join(t.TempDir(), "spool")
	if err := os.Mkdir(name, 0o755); err != nil {
		t.Fatal(err)
	}

	would := fileDirectory(&Ctx{Test: true}, name, map[string]any{"mode": "0700"})
	if !would.Ok || !would.Changed || !strings.Contains(would.Comment, "mode") {
		t.Fatalf("test mode did not report the pending chmod: %+v", would)
	}

	res := fileDirectory(&Ctx{}, name, map[string]any{"mode": "0700"})
	if !res.Ok || !res.Changed || res.Changes["mode"] != "0700" {
		t.Fatalf("mode drift not corrected: %+v", res)
	}
	st, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Errorf("mode = %o, want 0700", st.Mode().Perm())
	}

	again := fileDirectory(&Ctx{}, name, map[string]any{"mode": "0700"})
	if !again.Ok || again.Changed {
		t.Errorf("second run changed something: %+v", again)
	}
}

func TestFileDirectoryRejectsABadMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory modes are unix-only")
	}
	name := filepath.Join(t.TempDir(), "spool")
	res := fileDirectory(&Ctx{}, name, map[string]any{"mode": "worldwide"})
	if res.Ok {
		t.Fatalf("a bad mode string must fail, not be ignored: %+v", res)
	}
	if !strings.Contains(res.Comment, "invalid mode") {
		t.Errorf("unhelpful comment: %q", res.Comment)
	}
	if _, err := os.Stat(name); err == nil {
		t.Error("directory was created despite the bad mode")
	}
}

func TestFileAbsentRemovesADanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "current")
	if err := os.Symlink(filepath.Join(dir, "gone"), link); err != nil {
		t.Fatal(err)
	}

	res := fileAbsent(&Ctx{}, link, map[string]any{})
	if !res.Ok || !res.Changed {
		t.Fatalf("dangling symlink reported already absent: %+v", res)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("symlink still present")
	}
}

// symlinkTarget builds a 0600 file and a symlink pointing at it, which is
// what an unprivileged user plants at a path a root state manages.
func symlinkTarget(t *testing.T) (link, target string) {
	t.Helper()
	dir := t.TempDir()
	target = filepath.Join(dir, "sensitive")
	if err := os.WriteFile(target, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(dir, "managed")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link, target
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// TestFileManagedWillNotChmodThroughASymlink is the local privilege
// escalation this guard exists for: chmod follows a link, so a path an
// unprivileged user can pre-create would otherwise let a root state widen
// any file on the host.
func TestFileManagedWillNotChmodThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are unix-only")
	}
	link, target := symlinkTarget(t)

	res := fileManaged(&Ctx{}, link, map[string]any{"mode": "0644"})
	if res.Ok {
		t.Error("managing only the mode of a symlink must fail")
	}
	if !strings.Contains(res.Comment, "symlink") {
		t.Errorf("the comment should say why: %q", res.Comment)
	}
	if got := modeOf(t, target); got != 0o600 {
		t.Errorf("the link's target is now %o, want 0600", got)
	}
}

// TestFileManagedFollowsASymlinkWhenToldTo keeps the door open for a state
// that means it.
func TestFileManagedFollowsASymlinkWhenToldTo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are unix-only")
	}
	link, target := symlinkTarget(t)

	res := fileManaged(&Ctx{}, link, map[string]any{"mode": "0644", "follow_symlinks": "true"})
	if !res.Ok {
		t.Fatalf("follow_symlinks: true should be allowed: %+v", res)
	}
	if got := modeOf(t, target); got != 0o644 {
		t.Errorf("target mode = %o, want 0644", got)
	}
}

// TestFileManagedWithContentReplacesTheSymlink is the case that must keep
// working: the write goes to a temp file and the rename replaces the link,
// so the target is never touched and there is nothing to refuse.
func TestFileManagedWithContentReplacesTheSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are unix-only")
	}
	link, target := symlinkTarget(t)

	res := fileManaged(&Ctx{}, link, map[string]any{"contents": "managed", "mode": "0644"})
	if !res.Ok {
		t.Fatalf("writing content through a managed path must work: %+v", res)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("the link should have been replaced by a real file")
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "secret\n" {
		t.Errorf("the target was modified: %q %v", b, err)
	}
}

// TestFileDirectoryWillNotChmodThroughASymlink covers the same mistake on
// a directory, where the target is somebody else's whole tree.
func TestFileDirectoryWillNotChmodThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are unix-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "managed")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	res := fileDirectory(&Ctx{}, link, map[string]any{"mode": "0777"})
	if res.Ok {
		t.Error("managing the mode of a symlinked directory must fail")
	}
	if got := modeOf(t, target); got != 0o700 {
		t.Errorf("the target directory is now %o, want 0700", got)
	}
}
