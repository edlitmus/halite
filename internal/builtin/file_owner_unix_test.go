//go:build unix

package builtin

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// otherGroup is a group the test's account belongs to besides its primary
// one: the one ownership change an unprivileged account may make is to move
// its own file into such a group.
func otherGroup(t *testing.T) (int, string) {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range groups {
		if gid == os.Getgid() {
			continue
		}
		g, err := user.LookupGroupId(strconv.Itoa(gid))
		if err != nil {
			continue
		}
		return gid, g.Name
	}
	t.Skip("this account belongs to no group but its primary one, so no ownership change can be observed without root")
	return 0, ""
}

func gidOf(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return int(info.Sys().(*syscall.Stat_t).Gid)
}

// A requested group is applied after the contents are rewritten, even when
// the file it replaced already had it. Before, ownership was compared with
// the old file, found right, and not applied; the rewrite put a new file in
// place owned by whoever ran it, and on the estate Alertmanager lost read
// access to its own configuration. DIVERGENCE 5.253.
func TestARewriteKeepsTheRequestedGroup(t *testing.T) {
	gid, name := otherGroup(t)
	path := filepath.Join(t.TempDir(), "service.yml")
	if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, -1, gid); err != nil {
		t.Fatal(err)
	}

	r := New()
	res, err := r.States.Call(newCtx(false), "file.managed",
		value.MapOf("name", path, "contents", "new", "group", name, "mode", "0640"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("the rewrite did not happen: %+v", res)
	}
	if got := gidOf(t, path); got != gid {
		t.Errorf("after a rewrite the file's group is %d; the state asks for %s (%d)", got, name, gid)
	}
}

// A file whose state names no owner keeps the one it had through a
// rewrite, as Salt's in-place write does. DIVERGENCE 5.253.
func TestARewriteKeepsTheOwnerItReplaced(t *testing.T) {
	gid, _ := otherGroup(t)
	path := filepath.Join(t.TempDir(), "plain.conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, -1, gid); err != nil {
		t.Fatal(err)
	}

	r := New()
	res, err := r.States.Call(newCtx(false), "file.managed", value.MapOf("name", path, "contents", "new"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("the rewrite did not happen: %+v", res)
	}
	if got := gidOf(t, path); got != gid {
		t.Errorf("a rewrite with no owner requested moved the file from group %d to %d", gid, got)
	}
}
