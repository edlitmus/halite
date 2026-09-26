package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// `file.append` with a trailing newline in `text` converges.
//
// # Why this is not an exotic input
//
// A YAML block scalar always ends in a newline. Checked against this
// project's own parser: `text: |\n  Managed by halite\n` yields
// `"Managed by halite\n"`. So the ordinary spelling
//
//	/etc/motd:
//	  file.append:
//	    - text: |
//	        Managed by halite
//
// hands the state one line with a newline inside it.
//
// `fileAppendPrepend` decides what is missing by building a set of the
// file's lines, which have no newlines in them, and asking whether each
// requested line is in it. An element carrying its own newline is never in
// that set, so it is appended on every run and the file grows forever —
// a state that cannot converge, which is worse than one that fails,
// because nothing reports it. DIVERGENCE 5.157.
func TestFileAppendConvergesWithATrailingNewline(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "motd")
	if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	apply := func() string {
		t.Helper()
		if _, err := r.States.Call(newCtx(false), "file.append",
			value.MapOf("name", path, "text", []any{"Managed by halite\n"})); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	first := apply()
	second := apply()
	if first != second {
		t.Errorf("a second run changed the file, so this state never converges:\n"+
			"after one run:  %q\nafter two runs: %q", first, second)
	}
	if n := strings.Count(second, "Managed by halite"); n != 1 {
		t.Errorf("the line appears %d times after two runs, want 1:\n%q", n, second)
	}
}

// `file.prepend` shares the path, and the exec functions share it too.
//
// One fix for four entry points, so all four are checked: a fix applied to
// the state and not to `file.append`'s execution form would leave the
// churn reachable from `halite-node call`.
func TestPrependAndTheExecFormsConvergeToo(t *testing.T) {
	r := New()
	dir := t.TempDir()

	for _, c := range []struct{ name, fn, arg string }{
		{"prepend state", "file.prepend", "text"},
		{"append state", "file.append", "text"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-"))
			if err := os.WriteFile(path, []byte("body\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := r.States.Call(newCtx(false), c.fn,
					value.MapOf("name", path, c.arg, []any{"one\ntwo\n"})); err != nil {
					t.Fatal(err)
				}
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range []string{"one", "two"} {
				if n := strings.Count(string(body), line+"\n"); n != 1 {
					t.Errorf("%s appears %d times after two runs, want 1:\n%q",
						line, n, body)
				}
			}
		})
	}

	// The execution functions take the same path through
	// fileAppendPrepend, so they converge or they do not together.
	path := filepath.Join(dir, "exec-form")
	if err := os.WriteFile(path, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := r.Exec.Call(newCtx(false), "file.append",
			value.MapOf("path", path, "text", []any{"execline\n"})); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "execline"); n != 1 {
		t.Errorf("the exec form appended %d times, want 1:\n%q", n, body)
	}
}
