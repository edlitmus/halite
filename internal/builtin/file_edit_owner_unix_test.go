//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **Every writer keeps the file's group** (issue 321, M-20). Only
// file.managed did, after DIVERGENCE 5.253; on main every edit below came
// back in the account's primary group, which as root is `postgres:postgres
// 0640` becoming `root:root 0640` -- the 5.253 outage in each of them.
//
// Unprivileged, as the 5.253 tests are: the file is moved into one of the
// account's supplementary groups, which is the one ownership change such an
// account may make, and the edit must leave it there.
func TestEveryEditKeepsTheFilesGroup(t *testing.T) {
	gid, _ := otherGroup(t)
	cases := []struct {
		fn    string
		state bool
		args  func(path string) *value.Map
	}{
		{"file.replace", true, func(p string) *value.Map {
			return value.MapOf("name", p, "pattern", "old", "repl", "new")
		}},
		{"file.line", true, func(p string) *value.Map {
			return value.MapOf("name", p, "content", "added", "mode", "ensure", "after", "old")
		}},
		{"file.blockreplace", true, func(p string) *value.Map {
			return value.MapOf("name", p, "marker_start", "# start", "marker_end", "# end", "content", "block")
		}},
		{"file.append", true, func(p string) *value.Map {
			return value.MapOf("name", p, "text", []any{"appended"})
		}},
		{"file.prepend", true, func(p string) *value.Map {
			return value.MapOf("name", p, "text", []any{"prepended"})
		}},
		{"file.comment", true, func(p string) *value.Map {
			return value.MapOf("name", p, "regex", "^old")
		}},
		{"file.uncomment", true, func(p string) *value.Map {
			return value.MapOf("name", p, "regex", "^commented")
		}},
		{"file.serialize", true, func(p string) *value.Map {
			return value.MapOf("name", p, "dataset", value.MapOf("a", 1), "formatter", "json")
		}},
		{"file.sed", false, func(p string) *value.Map {
			return value.MapOf("path", p, "before", "old", "after", "new")
		}},
	}
	r := New()
	for _, tc := range cases {
		t.Run(tc.fn, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "service.conf")
			if err := os.WriteFile(path, []byte("old\n#commented\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(path, -1, gid); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			c := newCtx(false)
			if tc.state {
				res, err := r.States.Call(c, tc.fn, tc.args(path))
				if err != nil {
					t.Fatal(err)
				}
				if res.Result == nil || !*res.Result {
					t.Fatalf("%s failed: %s", tc.fn, res.Comment)
				}
			} else if _, err := r.Exec.Call(c, tc.fn, tc.args(path)); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(after) == string(before) {
				t.Fatalf("%s did not rewrite the file, so the test shows nothing", tc.fn)
			}
			if got := gidOf(t, path); got != gid {
				t.Errorf("after %s the file's group is %d; it was %d", tc.fn, got, gid)
			}
		})
	}
}

// writeAtomic itself, which is what the modules that rewrite /etc/hosts,
// sysctl.conf, fstab and the rest go through.
func TestWriteAtomicKeepsTheReplacedFilesGroup(t *testing.T) {
	gid, _ := otherGroup(t)
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, -1, gid); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gidOf(t, path); got != gid {
		t.Errorf("writeAtomic left the file in group %d; it was %d", got, gid)
	}
	// A new file is the writer's, as it always was.
	fresh := filepath.Join(t.TempDir(), "new")
	if err := writeAtomic(fresh, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gidOf(t, fresh); got == gid && gid != os.Getgid() {
		t.Errorf("a new file took the other group %d", got)
	}
}
