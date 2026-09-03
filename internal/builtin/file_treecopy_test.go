package builtin

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/value"
)

// recurseLab is a file server holding one subtree and a destination to
// copy it into.
type recurseLab struct {
	src  string
	dest string
	ctx  *exec.Context
}

func newRecurseLab(t *testing.T, files map[string]string) *recurseLab {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "tree")
	for name, body := range files {
		path := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &recurseLab{
		src:  src,
		dest: filepath.Join(root, "dest"),
		ctx: &exec.Context{
			Env:   "base",
			Files: fileserver.NewFetcher(fileserver.NewRoots(map[string][]string{"base": {src}})),
		},
	}
}

func (l *recurseLab) run(t *testing.T, args ...any) (comment string, ok bool) {
	t.Helper()
	all := append([]any{"name", l.dest, "source", "salt://app"}, args...)
	res, err := fileRecurse(l.ctx, value.MapOf(all...))
	if err != nil {
		t.Fatal(err)
	}
	return res.Comment, res.Succeeded()
}

func (l *recurseLab) placed(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(l.dest, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(l.dest, path)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out
}

// `file.recurse` copies a subtree of the file server onto a node. Four
// references in a real estate's tree, and without it a directory of
// files means one `file.managed` each, kept in step by hand.
func TestFileRecurseCopiesASubtree(t *testing.T) {
	l := newRecurseLab(t, map[string]string{
		"app/a.conf":     "one\n",
		"app/sub/b.conf": "two\n",
		"other/c.conf":   "not copied\n",
	})

	if _, ok := l.run(t); !ok {
		t.Fatal("the copy failed")
	}
	got := l.placed(t)
	want := []string{"a.conf", "sub/b.conf"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("placed %v, want %v — a sibling directory was copied or a "+
			"nested file was missed", got, want)
	}
}

// The second run reports no change, which is what makes it usable in a
// scheduled highstate.
func TestFileRecurseConverges(t *testing.T) {
	l := newRecurseLab(t, map[string]string{"app/a.conf": "one\n"})
	if _, ok := l.run(t); !ok {
		t.Fatal("the first run failed")
	}
	res, err := fileRecurse(l.ctx, value.MapOf("name", l.dest, "source", "salt://app"))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run reported changes: %s", res.Comment)
	}
}

// Test mode writes nothing and still says what it would do.
func TestFileRecurseInTestModeWritesNothing(t *testing.T) {
	l := newRecurseLab(t, map[string]string{"app/a.conf": "one\n"})
	l.ctx.Test = true

	res, err := fileRecurse(l.ctx, value.MapOf("name", l.dest, "source", "salt://app"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Error("test mode should leave the result undecided")
	}
	if !res.HasChanges() {
		t.Error("test mode should still report what would change")
	}
	if len(l.placed(t)) != 0 {
		t.Error("test mode wrote files")
	}
}

// `clean` removes what the source does not have, and nothing else.
func TestFileRecurseCleanRemovesOnlyStrangers(t *testing.T) {
	l := newRecurseLab(t, map[string]string{"app/a.conf": "one\n"})
	if _, ok := l.run(t); !ok {
		t.Fatal("the first run failed")
	}
	stray := filepath.Join(l.dest, "stray.conf")
	if err := os.WriteFile(stray, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok := l.run(t, "clean", true); !ok {
		t.Fatal("the clean run failed")
	}
	if got := l.placed(t); strings.Join(got, ",") != "a.conf" {
		t.Errorf("placed %v, want only a.conf", got)
	}
}

// Without `clean`, a file the source does not have is left alone: a
// state that silently deleted what it did not put there would be a bad
// surprise in a directory somebody else also writes to.
func TestFileRecurseLeavesStrangersWithoutClean(t *testing.T) {
	l := newRecurseLab(t, map[string]string{"app/a.conf": "one\n"})
	if _, ok := l.run(t); !ok {
		t.Fatal("the first run failed")
	}
	if err := os.WriteFile(filepath.Join(l.dest, "stray.conf"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.run(t); !ok {
		t.Fatal("the second run failed")
	}
	if len(l.placed(t)) != 2 {
		t.Error("a file the source does not have was removed without `clean`")
	}
}

// A source that lists nothing is reported rather than passed: it is more
// often a wrong path than an empty directory, and silently succeeding
// leaves a node missing the files somebody thought it had.
func TestFileRecurseRefusesAnEmptySource(t *testing.T) {
	l := newRecurseLab(t, map[string]string{"app/a.conf": "one\n"})
	res, err := fileRecurse(l.ctx, value.MapOf("name", l.dest, "source", "salt://nothing"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Error("an empty source should be reported, not passed")
	}
}

func TestFileRecurseFiltersWithIncludeAndExclude(t *testing.T) {
	files := map[string]string{"app/a.conf": "a\n", "app/b.txt": "b\n"}

	l := newRecurseLab(t, files)
	if _, ok := l.run(t, "include_pat", "*.conf"); !ok {
		t.Fatal("the include run failed")
	}
	if got := l.placed(t); strings.Join(got, ",") != "a.conf" {
		t.Errorf("include_pat placed %v, want only a.conf", got)
	}

	l2 := newRecurseLab(t, files)
	if _, ok := l2.run(t, "exclude_pat", "*.txt"); !ok {
		t.Fatal("the exclude run failed")
	}
	if got := l2.placed(t); strings.Join(got, ",") != "a.conf" {
		t.Errorf("exclude_pat placed %v, want only a.conf", got)
	}
}
