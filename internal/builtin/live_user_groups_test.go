package builtin

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `user.present`'s `groups`, on a real machine, on every platform that
// has a live leg: Linux (`usermod`), FreeBSD (`pw`) and macOS (`dscl` and
// `dseditgroup`, which `user.present` dispatches to on darwin).
//
// # Why this exists
//
// The three platforms did not agree on what a `groups:` list means.
// macOS only ever added. Linux and FreeBSD rewrote the whole
// supplementary list with `-G` -- but only on a run where the state had
// some other reason to call the account tool, because the diff looked
// for missing groups alone. So a group added by hand survived until the
// run where somebody changed the account's shell, and then it was gone
// (DIVERGENCE 5.135). Nothing had driven `user.present` against a real
// account on Linux or FreeBSD at all; the `user` evidence note said so.
//
// # What it establishes
//
//   - By default, `groups` is the groups the account must be in, and a
//     membership the tree does not name is left alone -- including on a
//     run that changes something else about the account.
//   - With `remove_groups: true` it is the complete supplementary set,
//     and a membership the tree does not name is removed.
//   - Both converge: the run after is a no-op.
//
// Membership is read with `id -Gn`, not through the module, so the
// module's reader is checked rather than trusted.
//
// # What it touches
//
// An account `halug<pid>` and three groups `halug<pid>a`..`c`, removed in
// a cleanup whether the body passed or not. Root and `HALITE_SYSTEM_LIVE=1`.

func TestLiveUserGroupsAreKeptOrRemovedAsAsked(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates an account")
	}
	if runtime.GOOS == "windows" {
		t.Skip("`user.present` groups here are the unix ones")
	}
	if os.Geteuid() != 0 {
		t.Skip("creating an account needs root; run it under sudo")
	}
	c := realCtx(t)
	r := New()

	account := fmt.Sprintf("halug%d", os.Getpid())
	ga, gb, gc := account+"a", account+"b", account+"c"
	if info, _ := r.Exec.Call(c, "user.info", value.MapOf("name", account)); info != nil {
		if m, ok := info.(*value.Map); ok && m.Len() > 0 {
			t.Fatalf("%s already exists on this machine; refusing to touch it", account)
		}
	}
	t.Cleanup(func() {
		if _, err := r.States.Call(c, "user.absent", value.MapOf("name", account, "purge", true)); err != nil {
			t.Errorf("removing %s: %v", account, err)
		}
		for _, g := range []string{ga, gb, gc} {
			if _, err := r.States.Call(c, "group.absent", value.MapOf("name", g)); err != nil {
				t.Errorf("removing %s: %v", g, err)
			}
		}
	})

	state := func(t *testing.T, args *value.Map) *value.Map {
		t.Helper()
		res, err := r.States.Call(c, "user.present", args)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() {
			t.Fatalf("user.present %v failed: %s", args, res.Comment)
		}
		if res.Changes == nil {
			return value.NewMap(0)
		}
		return res.Changes
	}
	spec := func(shell string, groups ...string) *value.Map {
		gs := make([]any, len(groups))
		for i, g := range groups {
			gs[i] = g
		}
		return value.MapOf("name", account, "shell", shell, "groups", gs,
			"home", "/tmp/"+account, "createhome", false, "fullname", "halite groups test")
	}

	for _, g := range []string{ga, gb, gc} {
		res, err := r.States.Call(c, "group.present", value.MapOf("name", g))
		if err != nil || !res.Succeeded() {
			t.Fatalf("group.present %s: %v %+v", g, err, res)
		}
	}
	state(t, spec("/bin/sh", ga, gb))
	if got := idGroups(t, c, account); !got[ga] || !got[gb] {
		t.Fatalf("the account was created in %v, want %s and %s", got, ga, gb)
	}

	// A membership nobody declared: somebody added it by hand.
	handAdd(t, c, gc, account)
	if got := idGroups(t, c, account); !got[gc] {
		t.Fatalf("adding %s by hand did not take: %v", gc, got)
	}

	t.Run("a hand-added group survives a run that changes something else", func(t *testing.T) {
		changes := state(t, spec("/usr/bin/false", ga, gb))
		if !changes.Has("shell") {
			t.Errorf("the shell change was not reported: %v", changes)
		}
		if changes.Has("groups") {
			t.Errorf("groups were reported as changing, and the account is in all it was asked for: %v", changes)
		}
		if got := idGroups(t, c, account); !got[gc] || !got[ga] || !got[gb] {
			t.Errorf("after a shell change the account is in %v; %s was taken away without being asked", got, gc)
		}
	})

	t.Run("a group the tree drops is kept by default", func(t *testing.T) {
		changes := state(t, spec("/usr/bin/false", ga))
		if changes.Len() != 0 {
			t.Errorf("dropping a group from the list reported a change by default: %v", changes)
		}
		if got := idGroups(t, c, account); !got[gb] {
			t.Errorf("without remove_groups, %s was removed: %v", gb, got)
		}
	})

	t.Run("remove_groups makes the list exact, then converges", func(t *testing.T) {
		args := spec("/usr/bin/false", ga)
		args.Set("remove_groups", true)

		c.Test = true
		predicted := state(t, args)
		c.Test = false
		if !predicted.Has("groups") {
			t.Errorf("test mode did not predict the removal: %v", predicted)
		}
		if got := idGroups(t, c, account); !got[gb] || !got[gc] {
			t.Errorf("test mode removed groups: %v", got)
		}

		changes := state(t, args)
		if !changes.Has("groups") {
			t.Errorf("removing %s and %s was not reported: %v", gb, gc, changes)
		}
		got := idGroups(t, c, account)
		if !got[ga] || got[gb] || got[gc] {
			t.Errorf("with remove_groups and groups [%s], the account is in %v", ga, got)
		}

		if again := state(t, args); again.Len() != 0 {
			t.Errorf("the run after remove_groups was not a no-op: %v", again)
		}
	})
}

// idGroups is the account's groups as `id -Gn` reports them, which is
// the platform's answer rather than the module's.
func idGroups(t *testing.T, c *exec.Context, account string) map[string]bool {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: []string{"id", "-Gn", account}})
	if err != nil {
		t.Fatalf("id -Gn %s: %v", account, err)
	}
	out := map[string]bool{}
	for _, g := range strings.Fields(res.Stdout) {
		out[g] = true
	}
	return out
}

// handAdd adds a membership with the platform's own tool and not the
// module, which is the membership an operator made and a tree never
// named.
func handAdd(t *testing.T, c *exec.Context, group, account string) {
	t.Helper()
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		argv = []string{"dseditgroup", "-o", "edit", "-a", account, "-t", "user", group}
	case "freebsd":
		argv = []string{"pw", "groupmod", group, "-m", account}
	default:
		argv = []string{"usermod", "-aG", group, account}
	}
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		t.Fatalf("%v: %v", argv, err)
	}
}
