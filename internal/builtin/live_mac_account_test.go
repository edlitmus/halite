package builtin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `mac_user`, `mac_group` and `mac_shadow`, driven against the real
// Open Directory on this Mac.
//
// # Why the three are one test
//
// They are one module's worth of machinery in three registrations, and
// the thing worth demonstrating spans all of them: an account is
// created with a group, given a password, and removed. Splitting that
// into three tests would mean three accounts, or one account and an
// ordering dependency between tests that Go does not promise. So the
// arc runs in order, in subtests, against a single throwaway account.
//
// # What it touches
//
// An account `halitet<pid>` and a group `halitetg<pid>`, neither of
// which exists on any Mac, and the home directory
// `/Users/halitet<pid>` that `createhomedir` makes for it. A cleanup
// removes all three whether the body passed or not. Nothing here reads
// or changes any other account.
//
// # Why it skips without root
//
// `dscl . -create`, `dseditgroup` and `dscl . -passwd` all need it, and
// all of them change Open Directory rather than a file this test could
// put back. `HALITE_SYSTEM_LIVE=1` on top, because a `go test ./...` on
// a Mac somebody is working on should not create an account. Run it:
//
//	sudo HALITE_SYSTEM_LIVE=1 go test -run TestLiveMacAccount -v ./internal/builtin/
//
// # What it establishes
//
// That the `dscl . -create` sequence produces an account this module's
// own reader then finds, with the uid, home, shell, real name and
// supplementary group that were asked for; that a second `user.present`
// with the same spec is a no-op rather than a second creation — the
// convergence question, which is the one a fixture cannot answer;
// that changing an attribute is seen as a change and then converges;
// that `mac_shadow.set_password` sets a password Open Directory
// reports as set; and that `user.absent` with `purge` removes the
// account and its home and is then converged.

func macAccountLiveRoot(t *testing.T) (*exec.Context, string, string) {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to create a real account on this Mac")
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("the mac account modules are macOS's, and this is %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		t.Skip("dscl -create, dseditgroup and dscl -passwd need root; run it under sudo")
	}
	c := realCtx(t)
	for _, tool := range []string{"dscl", "dseditgroup"} {
		if c.Which(tool) == "" {
			t.Fatalf("HALITE_SYSTEM_LIVE is set and there is no `%s`; this is not a Mac", tool)
		}
	}

	account := fmt.Sprintf("halitet%d", os.Getpid())
	group := fmt.Sprintf("halitetg%d", os.Getpid())

	// The account must not already exist, or the arc below would be
	// reporting on somebody else's record and the cleanup would remove
	// it.
	if info, err := macUserInfo(c, account); err == nil && info.Len() > 0 {
		t.Fatalf("%s already exists on this Mac; refusing to touch it", account)
	}

	t.Cleanup(func() {
		_ = macUserDelete(c, account, true)
		_ = macGroupDelete(c, group)
	})
	return c, account, group
}

func TestLiveMacAccountArc(t *testing.T) {
	c, account, group := macAccountLiveRoot(t)

	spec := func() *value.Map {
		return value.MapOf(
			"name", account,
			"home", "/Users/"+account,
			"shell", "/bin/zsh",
			"fullname", "halite selftest account",
			"groups", []any{group},
		)
	}

	t.Run("the group is created and then converges", func(t *testing.T) {
		args := value.MapOf("name", group)

		first, err := macGroupPresentState(c, args)
		if err != nil {
			t.Fatal(err)
		}
		if !first.Succeeded() || !first.HasChanges() {
			t.Fatalf("creating the group did not report a change: %+v", first)
		}

		info, err := macGroupInfo(c, group)
		if err != nil {
			t.Fatalf("reading the group back: %v", err)
		}
		if info.Len() == 0 {
			t.Fatalf("%s was reported created and this module cannot read it", group)
		}
		if gid, _ := info.Get("gid"); gid == nil || gid == int64(0) {
			t.Errorf("the group read back with gid %#v", gid)
		}

		second, err := macGroupPresentState(c, args)
		if err != nil {
			t.Fatal(err)
		}
		if !second.Succeeded() || second.HasChanges() {
			t.Errorf("a second group.present was not a no-op: %+v", second)
		}
	})

	t.Run("the account is created with what was asked for", func(t *testing.T) {
		res, err := macUserPresentState(c, spec())
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() || !res.HasChanges() {
			t.Fatalf("creating the account did not report a change: %+v", res)
		}

		info, err := macUserInfo(c, account)
		if err != nil {
			t.Fatalf("reading the account back: %v", err)
		}
		if info.Len() == 0 {
			t.Fatalf("%s was reported created and this module cannot read it", account)
		}

		if uid, _ := info.Get("uid"); uid == nil || uid == int64(0) {
			t.Errorf("uid read back as %#v", uid)
		}
		for _, want := range []struct{ key, value string }{
			{"home", "/Users/" + account},
			{"shell", "/bin/zsh"},
			{"fullname", "halite selftest account"},
		} {
			if got, _ := info.Get(want.key); got != want.value {
				t.Errorf("%s read back as %#v, want %q", want.key, got, want.value)
			}
		}

		// The supplementary group was asked for at creation, and the
		// question is whether Open Directory reports it back through the
		// same reader the state compares against on its next run.
		groups, err := macUserGroups(c, account)
		if err != nil {
			t.Fatalf("reading the account's groups: %v", err)
		}
		found := false
		for _, g := range groups {
			if value.KeyString(g) == group {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not in %v, and it was asked for at creation", group, groups)
		}
	})

	// The one a fixture cannot answer.
	t.Run("a second run with the same spec changes nothing", func(t *testing.T) {
		res, err := macUserPresentState(c, spec())
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() {
			t.Fatalf("a second user.present failed: %+v", res)
		}
		if res.HasChanges() {
			t.Errorf("a second user.present on an unchanged account reported %+v -- "+
				"this state cannot converge", res)
		}
	})

	t.Run("changing the shell is a change, and then converges", func(t *testing.T) {
		changed := spec()
		changed.Set("shell", "/bin/sh")

		first, err := macUserPresentState(c, changed)
		if err != nil {
			t.Fatal(err)
		}
		if !first.Succeeded() || !first.HasChanges() {
			t.Fatalf("changing the shell was not reported as a change: %+v", first)
		}

		info, err := macUserInfo(c, account)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := info.Get("shell"); got != "/bin/sh" {
			t.Errorf("the shell read back as %#v after being changed", got)
		}

		second, err := macUserPresentState(c, changed)
		if err != nil {
			t.Fatal(err)
		}
		if second.HasChanges() {
			t.Errorf("the run after a shell change still reported one: %+v", second)
		}
	})

	t.Run("mac_shadow sets a password Open Directory reports as set", func(t *testing.T) {
		before, err := macShadowInfo(c, account)
		if err != nil {
			t.Fatalf("reading the shadow record: %v", err)
		}
		if name, _ := before.Get("name"); name != account {
			t.Fatalf("the shadow record is for %#v, not %s", name, account)
		}
		if got, _ := before.Get("passwd"); got != nil {
			t.Logf("before set_password, passwd reads %q", got)
		}

		if err := macShadowSetPassword(c, account, throwawayPassword(t)); err != nil {
			t.Fatalf("set_password: %v", err)
		}

		after, err := macShadowInfo(c, account)
		if err != nil {
			t.Fatalf("reading the shadow record back: %v", err)
		}
		if got, _ := after.Get("passwd"); got != "set" {
			t.Errorf("after setting a password, passwd reads %#v, want %q", got, "set")
		}
	})

	t.Run("the account is removed with its home, and then converges", func(t *testing.T) {
		home := "/Users/" + account
		args := value.MapOf("name", account, "purge", true)

		first, err := macUserAbsentState(c, args)
		if err != nil {
			t.Fatal(err)
		}
		if !first.Succeeded() || !first.HasChanges() {
			t.Fatalf("removing the account did not report a change: %+v", first)
		}

		info, err := macUserInfo(c, account)
		if err != nil {
			t.Fatalf("reading a removed account errored rather than reporting absence: %v", err)
		}
		if info.Len() != 0 {
			t.Errorf("%s still reads back after being removed: %v", account, info)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Errorf("%s survived a purge (%v)", home, err)
		}

		second, err := macUserAbsentState(c, args)
		if err != nil {
			t.Fatal(err)
		}
		if !second.Succeeded() || second.HasChanges() {
			t.Errorf("user.absent on an already-absent account was not a no-op: %+v", second)
		}
	})

	t.Run("the group is removed, and then converges", func(t *testing.T) {
		args := value.MapOf("name", group)

		first, err := macGroupAbsentState(c, args)
		if err != nil {
			t.Fatal(err)
		}
		if !first.Succeeded() || !first.HasChanges() {
			t.Fatalf("removing the group did not report a change: %+v", first)
		}

		info, err := macGroupInfo(c, group)
		if err != nil {
			t.Fatalf("reading a removed group errored: %v", err)
		}
		if info.Len() != 0 {
			t.Errorf("%s still reads back after being removed: %v", group, info)
		}

		second, err := macGroupAbsentState(c, args)
		if err != nil {
			t.Fatal(err)
		}
		if !second.Succeeded() || second.HasChanges() {
			t.Errorf("group.absent on an already-absent group was not a no-op: %+v", second)
		}
	})
}

// throwawayPassword is random per run and is never printed. It is set on
// an account that exists for the length of this test and is removed with
// it.
//
// `dscl . -passwd` takes the password as an argument, so it is visible
// in `ps` for as long as the call runs -- which is true of this module
// in production too, not only here. Worth knowing; not something this
// test can avoid while driving the module as it is written.
func throwawayPassword(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generating a password: %v", err)
	}
	return "Ht-" + hex.EncodeToString(b)
}
