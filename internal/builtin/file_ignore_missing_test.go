package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `ignore_if_missing` on file.replace, which an estate's tree uses five
// times and this build did not have.
//
// It is how one tree covers several platforms: a state that hardens
// /etc/login.defs is written once and applied to a node that has no such
// file, where the edit is not a failure but a no-op. Salt's wording is
// "the state will simply report no changes", and that is the behaviour —
// success, not a warning, because nothing is wrong.
func TestFileReplaceIgnoresAMissingFileWhenAsked(t *testing.T) {
	r := New()
	c := &exec.Context{}
	absent := filepath.Join(t.TempDir(), "not-there.conf")

	args := func(ignore bool) *value.Map {
		return value.MapOf(
			"name", absent,
			"pattern", "^UMASK.*",
			"repl", "UMASK 027",
			"ignore_if_missing", ignore,
		)
	}

	// Set: success, no changes, and nothing created.
	res, err := r.States.Call(c, "file.replace", args(true))
	if err != nil {
		t.Fatalf("file.replace: %v", err)
	}
	if !stateSucceeded(res) {
		t.Errorf("ignore_if_missing did not succeed on a missing file: %v", res)
	}
	if res.Changes != nil && res.Changes.Len() != 0 {
		t.Errorf("a missing file reported changes: %v", res.Changes.Entries())
	}
	if _, err := os.Stat(absent); err == nil {
		t.Error("the file was created; ignore_if_missing means do nothing, not create")
	}

	// Unset: the default is still a failure, and the message says what
	// to do about it rather than only what went wrong.
	res, err = r.States.Call(c, "file.replace", args(false))
	if err != nil {
		t.Fatalf("file.replace: %v", err)
	}
	if stateSucceeded(res) {
		t.Error("a missing file succeeded without ignore_if_missing")
	}
	if !containsAll(res.Comment, "does not exist", "ignore_if_missing") {
		t.Errorf("the refusal does not offer the option: %q", res.Comment)
	}
}

// A file that *is* there is edited as before, so the option changes only
// the missing case.
func TestIgnoreIfMissingDoesNotChangeAnExistingFile(t *testing.T) {
	r := New()
	c := &exec.Context{}
	path := filepath.Join(t.TempDir(), "login.defs")
	writeFile(t, path, "UMASK 022\n")

	res, err := r.States.Call(c, "file.replace", value.MapOf(
		"name", path, "pattern", "^UMASK.*", "repl", "UMASK 027",
		"ignore_if_missing", true,
	))
	if err != nil {
		t.Fatalf("file.replace: %v", err)
	}
	if !stateSucceeded(res) {
		t.Fatalf("editing an existing file failed: %v", res)
	}
	if got := readFile(t, path); got != "UMASK 027\n" {
		t.Errorf("the file reads %q, want the replacement applied", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
