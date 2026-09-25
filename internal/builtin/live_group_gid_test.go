package builtin

import (
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `group.present`'s `gid`, on a real machine, on every platform with a
// live leg: Linux (`groupadd`), FreeBSD (`pw`) and macOS (`dseditgroup`,
// which `group.present` dispatches to on darwin).
//
// # Why this exists
//
// `mac_group`'s evidence note said an explicitly requested gid had never
// been run, nor the refusal to renumber a group that exists with a
// different one -- "which is the branch that protects every file the
// group owns". The Linux and FreeBSD paths make the same promise in the
// same words, and had not been run either.
//
// # What it establishes
//
//   - A group created with a gid has that gid, read through the
//     platform's own tool rather than this module.
//   - The run after is a no-op.
//   - A different gid for an existing group is refused, in test mode and
//     on a real run, and the group keeps its gid.
//   - A gid that already belongs to another group does not end with two
//     groups sharing it.
//
// # What it touches
//
// Two groups `halgid<pid>a` and `b` with gids from an unused block,
// removed in a cleanup. Root and `HALITE_SYSTEM_LIVE=1`.

func TestLiveGroupGidIsSetAndNeverRenumbered(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates groups")
	}
	if runtime.GOOS == "windows" {
		t.Skip("a gid is a unix idea")
	}
	if os.Geteuid() != 0 {
		t.Skip("creating a group needs root; run it under sudo")
	}
	c := realCtx(t)
	r := New()

	a := fmt.Sprintf("halgid%da", os.Getpid())
	b := fmt.Sprintf("halgid%db", os.Getpid())
	gid := freeGidPair(t)
	t.Logf("%s: using gids %d and %d", runtime.GOOS, gid, gid+1)
	t.Cleanup(func() {
		for _, g := range []string{a, b} {
			if _, err := r.States.Call(c, "group.absent", value.MapOf("name", g)); err != nil {
				t.Errorf("removing %s: %v", g, err)
			}
		}
	})

	present := func(t *testing.T, name string, gid int64, test bool) (bool, bool, string) {
		t.Helper()
		c.Test = test
		defer func() { c.Test = false }()
		res, err := r.States.Call(c, "group.present", value.MapOf("name", name, "gid", gid))
		if err != nil {
			t.Fatal(err)
		}
		return res.Succeeded(), res.HasChanges(), res.Comment
	}

	t.Run("created with the gid asked for", func(t *testing.T) {
		ok, changed, comment := present(t, a, gid, false)
		if !ok || !changed {
			t.Fatalf("group.present %s gid %d: ok=%v changed=%v %s", a, gid, ok, changed, comment)
		}
		if got := platformGid(t, c, a); got != gid {
			t.Errorf("%s was created with gid %d, asked for %d", a, got, gid)
		}
		if ok, changed, comment := present(t, a, gid, false); !ok || changed {
			t.Errorf("the run after was not a no-op: ok=%v changed=%v %s", ok, changed, comment)
		}
	})

	t.Run("an existing group is never renumbered", func(t *testing.T) {
		for _, test := range []bool{true, false} {
			ok, changed, comment := present(t, a, gid+1, test)
			if ok || changed {
				t.Errorf("test=%v: asking for gid %d on %s (gid %d) was not refused: %s",
					test, gid+1, a, gid, comment)
			}
			if !strings.Contains(comment, "renumber") {
				t.Errorf("test=%v: the refusal does not say why: %s", test, comment)
			}
		}
		if got := platformGid(t, c, a); got != gid {
			t.Errorf("a refused renumber moved %s from gid %d to %d", a, gid, got)
		}
	})

	t.Run("a gid another group has is not shared", func(t *testing.T) {
		ok, changed, comment := present(t, b, gid, false)
		t.Logf("%s: group.present %s with %s's gid %d: ok=%v changed=%v %q",
			runtime.GOOS, b, a, gid, ok, changed, comment)
		bGid, bExists := platformGidIfAny(t, c, b)
		t.Logf("%s: afterwards %s exists=%v gid=%d", runtime.GOOS, b, bExists, bGid)
		if bExists && bGid == gid {
			t.Errorf("%s and %s now share gid %d, so every file either owns belongs to both", a, b, gid)
		}
		if ok && !bExists {
			t.Errorf("group.present %s succeeded and there is no such group", b)
		}

		// A refused create must not leave a record behind. Checked against
		// the platform's list of groups, not a gid read: a record with no
		// gid reads as "no gid" and looks like no record at all.
		listed := groupListed(t, c, b)
		info, _ := r.Exec.Call(c, "group.info", value.MapOf("name", b))
		t.Logf("%s: after the refusal, %s listed=%v, group.info=%v", runtime.GOOS, b, listed, info)
		if !ok && listed {
			t.Errorf("group.present %s failed and left a group %s behind", b, b)
			if again, err := r.States.Call(c, "group.present", value.MapOf("name", b)); err == nil {
				t.Logf("and group.present %s without a gid then says: ok=%v changed=%v %q",
					b, again.Succeeded(), again.HasChanges(), again.Comment)
			}
		}
	})
}

// The record a refused `dseditgroup -o create -i <taken gid>` leaves on a
// Mac, made on purpose with the tool itself rather than the module (which
// now removes it) so that its shape is on the record for the unit
// fixture, and then removed.
func TestLiveMacGroupCreateWithATakenGidLeavesARecord(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" || runtime.GOOS != "darwin" || os.Geteuid() != 0 {
		t.Skip("macOS, root and HALITE_SYSTEM_LIVE=1")
	}
	c := realCtx(t)
	name := fmt.Sprintf("halgidrec%d", os.Getpid())
	t.Cleanup(func() {
		_, _ = c.Run(exec.Command{Argv: []string{"dseditgroup", "-o", "delete", name}, IgnoreExitCode: true})
	})
	res, err := c.Run(exec.Command{
		Argv:           []string{"dseditgroup", "-o", "create", "-i", "20", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dseditgroup -o create -i 20 %s: exit %d, stdout %q, stderr %q", name, res.Code, res.Stdout, res.Stderr)
	rec, err := c.Run(exec.Command{Argv: []string{"dscl", "-plist", ".", "-read", "/Groups/" + name}, IgnoreExitCode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dscl -plist . -read /Groups/%s: exit %d, stdout %q, stderr %q", name, rec.Code, rec.Stdout, rec.Stderr)
	if rec.Code != 0 {
		t.Skipf("this macOS leaves no record behind; the module's cleanup has nothing to do here")
	}
	if macGroupHasGid(c, name) {
		t.Errorf("the record a refused create left has a PrimaryGroupID: %q", rec.Stdout)
	}
}

// freeGidPair finds two consecutive gids no group on this machine has.
func freeGidPair(t *testing.T) int64 {
	t.Helper()
	for g := int64(42100); g < 42900; g += 2 {
		if _, err := user.LookupGroupId(strconv.FormatInt(g, 10)); err == nil {
			continue
		}
		if _, err := user.LookupGroupId(strconv.FormatInt(g+1, 10)); err == nil {
			continue
		}
		return g
	}
	t.Fatal("no two free gids in 42100..42900")
	return 0
}

func platformGid(t *testing.T, c *exec.Context, name string) int64 {
	t.Helper()
	gid, ok := platformGidIfAny(t, c, name)
	if !ok {
		t.Fatalf("the platform has no group %s", name)
	}
	return gid
}

// platformGidIfAny reads a group's gid with the platform's own tool:
// `dscl` on macOS, `getent` elsewhere.
func platformGidIfAny(t *testing.T, c *exec.Context, name string) (int64, bool) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		res, err := c.Run(exec.Command{
			Argv:           []string{"dscl", ".", "-read", "/Groups/" + name, "PrimaryGroupID"},
			IgnoreExitCode: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		// A record that is not there is not always a non-zero exit: on a
		// macOS 15.7.9 runner, `dscl . -read /Groups/<missing>
		// PrimaryGroupID` exited 0 and printed nothing on stdout. So an
		// empty answer is "no such group", and what dscl said is logged.
		if res.Code != 0 || strings.TrimSpace(res.Stdout) == "" {
			t.Logf("dscl -read /Groups/%s PrimaryGroupID: exit %d, stdout %q, stderr %q",
				name, res.Code, res.Stdout, res.Stderr)
			return 0, false
		}
		f := strings.Fields(res.Stdout)
		if len(f) < 2 {
			t.Fatalf("dscl -read %s PrimaryGroupID printed %q", name, res.Stdout)
		}
		n, err := strconv.ParseInt(f[len(f)-1], 10, 64)
		if err != nil {
			t.Fatalf("dscl -read %s PrimaryGroupID printed %q", name, res.Stdout)
		}
		return n, true
	}
	res, err := c.Run(exec.Command{Argv: []string{"getent", "group", name}, IgnoreExitCode: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 0 {
		return 0, false
	}
	f := strings.Split(strings.TrimSpace(res.Stdout), ":")
	if len(f) < 3 {
		t.Fatalf("getent group %s printed %q", name, res.Stdout)
	}
	n, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		t.Fatalf("getent group %s printed %q", name, res.Stdout)
	}
	return n, true
}

// groupListed reports whether the platform lists a group by that name,
// whatever attributes its record has.
func groupListed(t *testing.T, c *exec.Context, name string) bool {
	t.Helper()
	argv := []string{"getent", "group", name}
	if runtime.GOOS == "darwin" {
		argv = []string{"dscl", ".", "-list", "/Groups"}
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "darwin" {
		return res.Code == 0
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}
