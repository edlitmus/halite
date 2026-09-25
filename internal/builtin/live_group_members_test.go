package builtin

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `group.present`'s `members`, on a real machine, on every platform with
// a live leg: Linux, FreeBSD and macOS.
//
// # Why this exists
//
// `members` is the whole list: anyone not named is removed. The Linux
// path set it with gpasswd(1), which FreeBSD does not have, and the macOS
// path did not read `members` at all -- a tree listing members on a Mac
// was told the group already existed and nothing changed (DIVERGENCE
// 5.151). None of the three had been run.
//
// # What it establishes
//
// That a group's members become exactly the list, that the run after is
// a no-op, that a changed list adds and removes, that test mode predicts
// without acting, and that an empty list empties the group -- read with
// `getent group` or `dscl`, not through this module.
//
// # What it touches
//
// A group `halgm<pid>` and three accounts `halgm<pid>u1`..`u3`, removed in
// a cleanup. Root and `HALITE_SYSTEM_LIVE=1`.

func TestLiveGroupMembersAreExactlyTheList(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates a group and accounts")
	}
	if runtime.GOOS == "windows" {
		t.Skip("group members here are the unix ones")
	}
	if os.Geteuid() != 0 {
		t.Skip("creating a group needs root; run it under sudo")
	}
	c := realCtx(t)
	r := New()

	group := fmt.Sprintf("halgm%d", os.Getpid())
	u1, u2, u3 := group+"u1", group+"u2", group+"u3"
	t.Cleanup(func() {
		for _, u := range []string{u1, u2, u3} {
			if _, err := r.States.Call(c, "user.absent", value.MapOf("name", u)); err != nil {
				t.Errorf("removing %s: %v", u, err)
			}
		}
		if _, err := r.States.Call(c, "group.absent", value.MapOf("name", group)); err != nil {
			t.Errorf("removing %s: %v", group, err)
		}
	})
	if res, err := r.States.Call(c, "group.present", value.MapOf("name", group)); err != nil || !res.Succeeded() {
		t.Fatalf("group.present %s: %v %+v", group, err, res)
	}
	for _, u := range []string{u1, u2, u3} {
		res, err := r.States.Call(c, "user.present", value.MapOf("name", u, "shell", "/usr/bin/false",
			"home", "/tmp/"+u, "createhome", false))
		if err != nil || !res.Succeeded() {
			t.Fatalf("user.present %s: %v %+v", u, err, res)
		}
	}

	set := func(t *testing.T, test bool, members ...string) (bool, bool, string) {
		t.Helper()
		ms := make([]any, len(members))
		for i, m := range members {
			ms[i] = m
		}
		c.Test = test
		defer func() { c.Test = false }()
		res, err := r.States.Call(c, "group.present", value.MapOf("name", group, "members", ms))
		if err != nil {
			t.Fatal(err)
		}
		return res.Succeeded(), res.HasChanges(), res.Comment
	}
	expect := func(t *testing.T, want ...string) {
		t.Helper()
		got := platformMembers(t, c, group)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s holds %v, want %v", group, got, want)
		}
	}

	t.Run("members become exactly the list, then converge", func(t *testing.T) {
		ok, changed, comment := set(t, false, u1, u2)
		t.Logf("%s: members [u1 u2]: ok=%v changed=%v %q", runtime.GOOS, ok, changed, comment)
		if !ok || !changed {
			t.Errorf("setting members: ok=%v changed=%v %s", ok, changed, comment)
		}
		expect(t, u1, u2)
		if ok, changed, comment := set(t, false, u1, u2); !ok || changed {
			t.Errorf("the run after was not a no-op: ok=%v changed=%v %s", ok, changed, comment)
		}
	})

	t.Run("test mode predicts and does not act", func(t *testing.T) {
		ok, changed, comment := set(t, true, u2, u3)
		if !ok || !changed {
			t.Errorf("test mode did not predict a change: ok=%v changed=%v %s", ok, changed, comment)
		}
		expect(t, u1, u2)
	})

	t.Run("a changed list adds and removes", func(t *testing.T) {
		if ok, changed, comment := set(t, false, u2, u3); !ok || !changed {
			t.Errorf("changing members: ok=%v changed=%v %s", ok, changed, comment)
		}
		expect(t, u2, u3)
	})

	t.Run("an empty list empties the group", func(t *testing.T) {
		if ok, changed, comment := set(t, false); !ok || !changed {
			t.Errorf("emptying members: ok=%v changed=%v %s", ok, changed, comment)
		}
		expect(t)
	})
}

// platformMembers is a group's member list as the platform's own tool
// reports it: `getent group` elsewhere, `dscl` on macOS.
func platformMembers(t *testing.T, c *exec.Context, group string) []string {
	t.Helper()
	var out []string
	if runtime.GOOS == "darwin" {
		res, err := c.Run(exec.Command{
			Argv:           []string{"dscl", ".", "-read", "/Groups/" + group, "GroupMembership"},
			IgnoreExitCode: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		// "GroupMembership: a b", or "No such key: GroupMembership" on
		// stderr for a group with none.
		if f := strings.Fields(res.Stdout); len(f) > 1 && f[0] == "GroupMembership:" {
			out = f[1:]
		}
	} else {
		res, err := c.Run(exec.Command{Argv: []string{"getent", "group", group}})
		if err != nil {
			t.Fatal(err)
		}
		f := strings.Split(strings.TrimSpace(res.Stdout), ":")
		if len(f) == 4 && f[3] != "" {
			out = strings.Split(f[3], ",")
		}
	}
	sort.Strings(out)
	return out
}
