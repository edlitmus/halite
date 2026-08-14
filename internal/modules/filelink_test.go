package modules

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSymlinkIsCreatedThenLeftAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege Windows does not hand out")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": link, "target": target}

	if r := fileSymlink(&Ctx{}, "l", args); !r.Changed {
		t.Fatalf("want the link created: %+v", r)
	}
	got, err := os.Readlink(link)
	if err != nil || got != target {
		t.Fatalf("want a link to %s, got %q (%v)", target, got, err)
	}
	if r := fileSymlink(&Ctx{}, "l", args); r.Changed {
		t.Fatalf("a second run should be a no-op: %+v", r)
	}
}

func TestSymlinkIsRepointed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no symlinks")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "old"), link); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "new")

	r := fileSymlink(&Ctx{}, "l", map[string]any{"name": link, "target": want})
	if !r.Changed {
		t.Fatalf("a link pointing elsewhere is drift: %+v", r)
	}
	if got, _ := os.Readlink(link); got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestSymlinkWillNotReplaceARealFileWithoutForce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no symlinks")
	}
	dir := t.TempDir()
	inTheWay := filepath.Join(dir, "link")
	if err := os.WriteFile(inTheWay, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": inTheWay, "target": filepath.Join(dir, "target")}

	r := fileSymlink(&Ctx{}, "l", args)
	if r.Ok || !strings.Contains(r.Comment, "force") {
		t.Fatalf("a real file must not be deleted silently: %+v", r)
	}
	if body, _ := os.ReadFile(inTheWay); string(body) != "precious" {
		t.Fatal("the file should still be there")
	}

	args["force"] = "true"
	if r := fileSymlink(&Ctx{}, "l", args); !r.Changed {
		t.Fatalf("force should replace it: %+v", r)
	}
	if _, err := os.Readlink(inTheWay); err != nil {
		t.Fatalf("want a link now: %v", err)
	}
}

func TestSymlinkDryRunCreatesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no symlinks")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	r := fileSymlink(&Ctx{Test: true}, "l", map[string]any{"name": link, "target": filepath.Join(dir, "t")})
	if !r.Ok || !r.Changed {
		t.Fatalf("a dry run should report the pending link: %+v", r)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("a dry run must not create the link")
	}
}

func TestSymlinkNeedsATarget(t *testing.T) {
	if r := fileSymlink(&Ctx{}, "l", map[string]any{"name": "/tmp/x"}); r.Ok {
		t.Fatal("a link with no target should fail the state")
	}
}

func TestCopyMakesAndKeepsACopy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "dest")
	if err := os.WriteFile(source, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": dest, "source": source}

	if r := fileCopy(&Ctx{}, "c", args); !r.Changed {
		t.Fatalf("want the copy made: %+v", r)
	}
	if got := read(t, dest); got != "one\n" {
		t.Fatalf("unexpected contents %q", got)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(dest)
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Fatalf("the source's mode should carry over, got %04o", perm)
		}
	}
	if r := fileCopy(&Ctx{}, "c", args); r.Changed {
		t.Fatalf("a second run should be a no-op: %+v", r)
	}

	if err := os.WriteFile(source, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := fileCopy(&Ctx{}, "c", args); !r.Changed {
		t.Fatalf("a changed source should be copied again: %+v", r)
	}
}

func TestCopyRefusesWhatItCannotCopy(t *testing.T) {
	dir := t.TempDir()
	if r := fileCopy(&Ctx{}, "c", map[string]any{"name": filepath.Join(dir, "d"), "source": filepath.Join(dir, "absent")}); r.Ok {
		t.Fatal("a missing source should fail the state")
	}
	r := fileCopy(&Ctx{}, "c", map[string]any{"name": filepath.Join(dir, "d"), "source": dir})
	if r.Ok || !strings.Contains(r.Comment, "file.recurse") {
		t.Fatalf("a directory source should point at file.recurse: %+v", r)
	}
}

func TestCopyRespectsForceFalse(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "dest")
	for path, body := range map[string]string{source: "new\n", dest: "old\n"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := fileCopy(&Ctx{}, "c", map[string]any{"name": dest, "source": source, "force": "false"})
	if r.Ok {
		t.Fatalf("force: false should refuse to overwrite: %+v", r)
	}
	if got := read(t, dest); got != "old\n" {
		t.Fatalf("the destination should be untouched, got %q", got)
	}
}
