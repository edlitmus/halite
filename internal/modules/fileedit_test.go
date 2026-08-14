package modules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// editFixture writes a file to edit and returns its path.
func editFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conf")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAppendAddsOnlyMissingLines(t *testing.T) {
	path := editFixture(t, "sshd_enable=\"YES\"\n")
	args := map[string]any{"name": path, "text": []any{"sshd_enable=\"YES\"", "nginx_enable=\"YES\""}}

	r := fileAppend(&Ctx{}, "rc", args)
	if !r.Changed {
		t.Fatalf("the missing line should be added: %+v", r)
	}
	if got := read(t, path); got != "sshd_enable=\"YES\"\nnginx_enable=\"YES\"\n" {
		t.Fatalf("unexpected contents %q", got)
	}
	if r := fileAppend(&Ctx{}, "rc", args); r.Changed {
		t.Fatalf("a second run should be a no-op: %+v", r)
	}
}

func TestPrependPutsLinesFirst(t *testing.T) {
	path := editFixture(t, "second\n")
	r := filePrepend(&Ctx{}, "f", map[string]any{"name": path, "text": "first"})
	if !r.Changed {
		t.Fatalf("want a change: %+v", r)
	}
	if got := read(t, path); got != "first\nsecond\n" {
		t.Fatalf("unexpected contents %q", got)
	}
}

func TestAppendCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.conf")
	if r := fileAppend(&Ctx{}, "f", map[string]any{"name": path, "text": "hello"}); !r.Changed {
		t.Fatalf("want a change: %+v", r)
	}
	if got := read(t, path); got != "hello\n" {
		t.Fatalf("unexpected contents %q", got)
	}
}

func TestEditKeepsTheExistingMode(t *testing.T) {
	path := editFixture(t, "one\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	fileAppend(&Ctx{}, "f", map[string]any{"name": path, "text": "two"})

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("an edit must not loosen the mode: got %04o", perm)
	}
}

func TestEditDryRunWritesNothing(t *testing.T) {
	path := editFixture(t, "one\n")
	r := fileAppend(&Ctx{Test: true}, "f", map[string]any{"name": path, "text": "two"})
	if !r.Ok || !r.Changed {
		t.Fatalf("a dry run should report the pending change: %+v", r)
	}
	if got := read(t, path); got != "one\n" {
		t.Fatalf("a dry run must not write: %q", got)
	}
}

func TestEditReportsADiff(t *testing.T) {
	path := editFixture(t, "one\n")
	r := fileAppend(&Ctx{}, "f", map[string]any{"name": path, "text": "two"})
	if !strings.Contains(r.Changes["diff"], "+two") {
		t.Fatalf("want the added line in the diff, got %q", r.Changes["diff"])
	}
	r = fileAppend(&Ctx{}, "f", map[string]any{"name": path, "text": "three", "show_diff": "false"})
	if _, ok := r.Changes["diff"]; ok {
		t.Fatal("show_diff: false should suppress the diff")
	}
}

func TestCommentAndUncomment(t *testing.T) {
	path := editFixture(t, "PermitRootLogin yes\nPort 22\n")
	args := map[string]any{"name": path, "regex": "^PermitRootLogin yes"}

	if r := fileComment(&Ctx{}, "sshd", args); !r.Changed {
		t.Fatalf("want a change: %+v", r)
	}
	if got := read(t, path); got != "#PermitRootLogin yes\nPort 22\n" {
		t.Fatalf("unexpected contents %q", got)
	}
	if r := fileComment(&Ctx{}, "sshd", args); r.Changed {
		t.Fatalf("an already-commented line should not be commented twice: %+v", r)
	}
	if r := fileUncomment(&Ctx{}, "sshd", args); !r.Changed {
		t.Fatalf("uncomment should restore it: %+v", r)
	}
	if got := read(t, path); got != "PermitRootLogin yes\nPort 22\n" {
		t.Fatalf("unexpected contents %q", got)
	}
}

func TestCommentNeedsAValidRegex(t *testing.T) {
	path := editFixture(t, "x\n")
	if r := fileComment(&Ctx{}, "f", map[string]any{"name": path, "regex": "^[unclosed"}); r.Ok {
		t.Fatal("an invalid regex should fail the state")
	}
	if r := fileComment(&Ctx{}, "f", map[string]any{"name": path}); r.Ok {
		t.Fatal("comment with no regex should fail the state")
	}
}

func TestCommentOnAMissingFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if r := fileComment(&Ctx{}, "f", map[string]any{"name": path, "regex": "x"}); r.Ok {
		t.Fatal("there is nothing to comment in a file that is not there")
	}
}
