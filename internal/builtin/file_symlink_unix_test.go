//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// linkedFile makes sites-available/app.conf and a link to it in
// sites-enabled, the shape the finding names.
func linkedFile(t *testing.T, contents string) (link, target string) {
	t.Helper()
	dir := t.TempDir()
	target = filepath.Join(dir, "sites-available", "app.conf")
	link = filepath.Join(dir, "sites-enabled", "app.conf")
	for _, d := range []string{filepath.Dir(target), filepath.Dir(link)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(target, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../sites-available/app.conf", link); err != nil {
		t.Fatal(err)
	}
	return link, target
}

func assertStillALink(t *testing.T, link, target, want string) {
	t.Helper()
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s was replaced by a %s", link, info.Mode())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("the target holds %q, want %q", got, want)
	}
	if ti, _ := os.Stat(target); ti.Mode().Perm() != 0o640 {
		t.Errorf("the target's mode became %o", ti.Mode().Perm())
	}
}

// **file.managed writes through a link** (issue 321, M-19). On main it
// renamed a new file over the link, with the link's own 0777, left the
// real configuration stale, and called the next run converged.
func TestFileManagedWritesThroughALink(t *testing.T) {
	link, target := linkedFile(t, "old\n")
	r := New()
	for run := 1; run <= 2; run++ {
		res, err := r.States.Call(newCtx(false), "file.managed", value.MapOf("name", link, "contents", "new\n"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || !*res.Result {
			t.Fatalf("run %d: %s", run, res.Comment)
		}
		if run == 2 && res.HasChanges() {
			t.Errorf("the second run changed something: %s", res.Comment)
		}
	}
	assertStillALink(t, link, target, "new\n")
}

// And every edit, which read through the link and then wrote over it.
func TestEditsWriteThroughALink(t *testing.T) {
	cases := []struct {
		fn    string
		state bool
		args  func(string) *value.Map
		want  string
	}{
		{"file.replace", true, func(p string) *value.Map { return value.MapOf("name", p, "pattern", "old", "repl", "new") }, "new\n"},
		{"file.append", true, func(p string) *value.Map { return value.MapOf("name", p, "text", []any{"more"}) }, "old\nmore\n"},
		{"file.line", true, func(p string) *value.Map {
			return value.MapOf("name", p, "content", "added", "mode", "ensure", "after", "old")
		}, "old\nadded\n"},
		{"file.serialize", true, func(p string) *value.Map {
			return value.MapOf("name", p, "dataset", value.MapOf("a", 1), "formatter", "json")
		}, "{\n  \"a\": 1\n}\n"},
		{"file.sed", false, func(p string) *value.Map { return value.MapOf("path", p, "before", "old", "after", "new") }, "new\n"},
	}
	r := New()
	for _, tc := range cases {
		t.Run(tc.fn, func(t *testing.T) {
			link, target := linkedFile(t, "old\n")
			if tc.state {
				res, err := r.States.Call(newCtx(false), tc.fn, tc.args(link))
				if err != nil {
					t.Fatal(err)
				}
				if res.Result == nil || !*res.Result {
					t.Fatalf("%s: %s", tc.fn, res.Comment)
				}
			} else if _, err := r.Exec.Call(newCtx(false), tc.fn, tc.args(link)); err != nil {
				t.Fatal(err)
			}
			assertStillALink(t, link, target, tc.want)
		})
	}
}

// A link to nothing is refused by name, not replaced.
func TestADanglingLinkIsRefused(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "app.conf")
	if err := os.Symlink(filepath.Join(dir, "gone"), link); err != nil {
		t.Fatal(err)
	}
	res, err := New().States.Call(newCtx(false), "file.managed", value.MapOf("name", link, "contents", "x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result || !strings.Contains(res.Comment, "does not resolve") {
		t.Errorf("a dangling link: %+v", res)
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Error("the dangling link was replaced")
	}
}
