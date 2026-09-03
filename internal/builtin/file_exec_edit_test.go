package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// callFileExec runs one of the file edit execution functions.
func callFileExec(t *testing.T, name string, args *value.Map) (any, error) {
	t.Helper()
	r := New()
	return r.Exec.Call(&exec.Context{}, "file."+name, args)
}

// Six file edits were states and not execution functions, so
// `salt['file.line'](…)` in a template and `halite-node call
// file.comment` both failed where Salt succeeds.
//
// `file.managed`, `file.absent` and `file.directory` are deliberately
// not among them. They are states in Salt too — checked against the Salt
// on this host rather than assumed — so a report calling them missing
// execution functions was wrong about what Salt does.
func TestTheFileEditsAreExecutionFunctions(t *testing.T) {
	for _, name := range []string{
		"touch", "symlink", "line", "blockreplace", "comment", "uncomment",
	} {
		if _, ok := New().Exec.Signatures().Lookup("file." + name); !ok {
			t.Errorf("file.%s is not an execution function", name)
		}
	}
	for _, name := range []string{"managed", "absent", "directory"} {
		if _, ok := New().Exec.Signatures().Lookup("file." + name); ok {
			t.Errorf("file.%s is an execution function here and is a state in Salt", name)
		}
	}
}

func TestFileCommentAndUncommentAsExec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha\n#beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := callFileExec(t, "comment",
		value.MapOf("path", path, "regex", "^alpha")); err != nil {
		t.Fatal(err)
	}
	if _, err := callFileExec(t, "uncomment",
		value.MapOf("path", path, "regex", "beta")); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "#alpha\nbeta\n" {
		t.Errorf("got %q, want %q", got, "#alpha\nbeta\n")
	}
}

// Salt names these `path`, and the states here name it `name`. A tree
// already writes Salt's spelling, so that is what the execution function
// takes.
func TestTheFileEditsTakeSaltsArgumentNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := callFileExec(t, "comment",
		value.MapOf("path", path, "regex", "^x")); err != nil {
		t.Fatalf("`path` is not accepted, and it is what Salt calls this: %v", err)
	}
}

// `file.symlink` reverses its pair in Salt: the target comes first and
// the link second, which is the opposite of the state's reading. Getting
// this backwards makes a link pointing the wrong way rather than an
// error, so it is worth its own test.
func TestFileSymlinkAsExecTakesTargetFirst(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := callFileExec(t, "symlink",
		value.MapOf("src", target, "path", link)); err != nil {
		t.Fatal(err)
	}

	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("%s is not a symlink: %v", link, err)
	}
	if got != target {
		t.Errorf("the link points at %q, want %q — the pair is the wrong way round",
			got, target)
	}
}

// An execution function that returns false for both "done" and
// "refused" makes the caller guess, and a template calling one has no
// other channel.
func TestAFailedFileEditIsAnErrorNotFalse(t *testing.T) {
	_, err := callFileExec(t, "comment",
		value.MapOf("path", filepath.Join(t.TempDir(), "missing.txt"), "regex", "^x"))
	if err == nil {
		t.Fatal("editing a file that does not exist returned no error")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Error("the error says nothing about what went wrong")
	}
}

// pkg.purged is pkg.removed that takes the configuration with it. The
// provider interface has carried the flag since it was written; only the
// state was missing, so a tree that purged had to be rewritten to merely
// remove — which leaves the configuration behind and is a different
// outcome, not a smaller one.
func TestPkgPurgedIsRegisteredSeparatelyFromRemoved(t *testing.T) {
	sigs := New().States.Signatures()
	for _, name := range []string{"pkg.removed", "pkg.purged"} {
		if _, ok := sigs.Lookup(name); !ok {
			t.Errorf("%s is not a state function", name)
		}
	}
}

// test.show_notification puts a message in a run's output and changes
// nothing. Four references in a real estate's tree.
func TestShowNotificationReportsItsText(t *testing.T) {
	r := New()
	mod, ok := r.States.Lookup("test.show_notification")
	if !ok {
		t.Fatal("test.show_notification is not a state function")
	}
	res, err := mod.Fn(&exec.Context{}, value.MapOf("text", "converging"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Errorf("it should succeed: %+v", res)
	}
	if res.Comment != "converging" {
		t.Errorf("comment = %q, want the text it was given", res.Comment)
	}
	if res.HasChanges() {
		t.Error("it changes nothing, and should report none")
	}
}
