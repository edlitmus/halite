//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// **file.recurse applies file_mode to a tree already copied** (issue
// 321, M-22). Mode and owner were set only on files the run rewrote, so a
// second run with a new file_mode said "already matches" and left 0644.
// And a rewritten file with no file_mode keeps its mode.
func TestFileRecurseCorrectsTheModeOfMatchingFiles(t *testing.T) {
	l := newRecurseLab(t, map[string]string{"app/a.conf": "a\n", "app/b.conf": "b\n"})
	if _, ok := l.run(t); !ok {
		t.Fatal("the first copy failed")
	}
	comment, ok := l.run(t, "file_mode", "0600")
	if !ok {
		t.Fatal(comment)
	}
	for _, name := range []string{"a.conf", "b.conf"} {
		info, err := os.Stat(filepath.Join(l.dest, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s is %o after file_mode '0600' (%s)", name, info.Mode().Perm(), comment)
		}
	}
	if comment, _ := l.run(t, "file_mode", "0600"); !strings.Contains(comment, "already matches") {
		t.Errorf("the third run did not converge: %s", comment)
	}

	// A content change with no file_mode keeps the file's own mode.
	if err := os.WriteFile(filepath.Join(l.src, "app", "a.conf"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.run(t); !ok {
		t.Fatal("the rewrite failed")
	}
	if info, _ := os.Stat(filepath.Join(l.dest, "a.conf")); info.Mode().Perm() != 0o600 {
		t.Errorf("a rewritten 0600 file became %o", info.Mode().Perm())
	}
}
