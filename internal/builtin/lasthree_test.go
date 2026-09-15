package builtin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// staticFileServer serves a directory as if it were the file server,
// which is all file.recurse needs: a listing and a local path per entry.
type staticFileServer struct{ root string }

func (s staticFileServer) rel(uri string) string {
	return strings.TrimPrefix(strings.TrimPrefix(uri, "salt://"), "/")
}

func (s staticFileServer) Fetch(_, uri string) (string, error) {
	p := filepath.Join(s.root, filepath.FromSlash(s.rel(uri)))
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s: %w", uri, err)
	}
	return p, nil
}

func (s staticFileServer) Hash(string, string) (string, string, error) {
	return "", "", fmt.Errorf("not needed here")
}

func (s staticFileServer) Exists(_, uri string) bool {
	_, err := os.Stat(filepath.Join(s.root, filepath.FromSlash(s.rel(uri))))
	return err == nil
}

func (s staticFileServer) ListUnder(_, prefix string) ([]string, error) {
	base := filepath.Join(s.root, filepath.FromSlash(s.rel(prefix)))
	var out []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		r, rerr := filepath.Rel(base, path)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(r))
		return nil
	})
	return out, err
}

// `cmd.run` with `bg` starts a command and does not wait. The estate's
// own aide state uses it for a database rebuild that takes minutes, so
// the test is the one thing that matters about it: the state returns
// while the command is still running.
func TestCmdRunBackgroundDoesNotWait(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("this needs a POSIX shell")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "done")

	r := New()
	start := time.Now()
	res, err := r.States.Call(newRunningCtx(), "cmd.run", value.MapOf(
		"name", "sleep 3; touch "+marker))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("%q", res.Comment)
	}
	waited := time.Since(start)

	// Without bg the call above waits three seconds. This is the control
	// for the assertion below, so that a machine slow enough to make the
	// timing meaningless fails here rather than passing by accident.
	if waited < 2*time.Second {
		t.Fatalf("the foreground run returned in %v, so the timing says nothing", waited)
	}

	marker2 := filepath.Join(dir, "done2")
	start = time.Now()
	res, err = r.States.Call(newRunningCtx(), "cmd.run", value.MapOf(
		"name", "sleep 3; touch "+marker2, "bg", true))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("%q", res.Comment)
	}
	if backgrounded := time.Since(start); backgrounded > time.Second {
		t.Errorf("bg waited %v for a three-second command", backgrounded)
	}
	if _, err := os.Stat(marker2); err == nil {
		t.Error("the background command had already finished, so it was not backgrounded")
	}
	if _, ok := res.Changes.Get("pid"); !ok {
		t.Errorf("no pid was reported: %v", res.Changes.StringKeys())
	}
}

// bg and timeout together are refused. Nothing waits for the process, so
// nothing can stop it at a deadline, and a tree that asked for a bounded
// run would have been given an unbounded one.
func TestCmdRunRefusesBackgroundWithTimeout(t *testing.T) {
	r := New()
	res, err := r.States.Call(newRunningCtx(), "cmd.run", value.MapOf(
		"name", "true", "bg", true, "timeout", "30s"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Error("bg with a timeout was accepted")
	}
	if !strings.Contains(res.Comment, "timeout") {
		t.Errorf("the refusal does not say why: %q", res.Comment)
	}
}

// file.recurse renders each file when the state names an engine, and --
// the part that decides whether a highstate converges -- compares the
// rendered output rather than the template source. A template compared
// unrendered differs from its own output on every run.
func TestFileRecurseTemplateConverges(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src")
	dest := filepath.Join(dir, "dest")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "conf"),
		[]byte("os = {{ grains['os'] }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New()
	c := newRunningCtx()
	c.Files = staticFileServer{root: source}

	args := value.MapOf("name", dest, "source", "salt://", "template", "jinja")

	res, err := r.States.Call(c, "file.recurse", args)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("first run: ok=%v comment=%q", res.Succeeded(), res.Comment)
	}

	body, err := os.ReadFile(filepath.Join(dest, "conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "{{") {
		t.Errorf("the file was copied unrendered: %q", body)
	}
	if !strings.Contains(string(body), "Ubuntu") {
		t.Errorf("the template did not see the grains: %q", body)
	}

	// The second run must find nothing to do. If the comparison used the
	// template source it would differ from the rendered file forever.
	res, err = r.States.Call(c, "file.recurse", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run rewrote the file: %q %v", res.Comment, res.Changes.StringKeys())
	}
}
