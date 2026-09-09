package builtin

import (
	"os/user"
	"runtime"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// mac_user / mac_group reads, against the real `dscl` on this Mac.
//
// # Why it needs no gate
//
// Every call here reads: `dscl -plist . -read` and `dscl . -list`. The
// writes — `dscl . -create`, `dseditgroup`, `dscl . -passwd` — need root
// and change Open Directory, so they are not exercised and no CI leg is
// a Mac. `evidence.go` records the three modules `assumed`.
//
// # What it establishes
//
// That `dscl -plist . -read` on a real account has the attributes
// macUserInfo reads, under the names it looks for, and that the plist
// reader borrowed from mac_defaults handles dscl's output; that a group
// reads back with a gid; that `user.present` in test mode against a
// name that is not there predicts a creation and runs nothing.

func TestLiveMacUserReadsThisMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_user is macOS's, and this is %s", runtime.GOOS)
	}
	c := realCtx(t)
	if c.Which("dscl") == "" {
		t.Skip("no `dscl` on this machine")
	}

	me, err := user.Current()
	if err != nil {
		t.Fatalf("who am I: %v", err)
	}

	info, err := macUserInfo(c, me.Username)
	if err != nil {
		t.Fatalf("macUserInfo(%q): %v", me.Username, err)
	}
	if info.Len() == 0 {
		t.Fatalf("macUserInfo(%q) found nothing for the running user", me.Username)
	}
	if v, _ := info.Get("uid"); v != int64(mustAtoi(t, me.Uid)) {
		t.Errorf("uid read as %#v, os/user says %s", v, me.Uid)
	}
	for _, k := range []string{"home", "shell"} {
		if v, _ := info.Get(k); v == "" || v == nil {
			t.Errorf("%s read as empty", k)
		}
	}

	// staff (gid 20) is on every Mac.
	g, err := macGroupInfo(c, "staff")
	if err != nil {
		t.Fatalf("macGroupInfo(staff): %v", err)
	}
	if v, _ := g.Get("gid"); v != int64(20) {
		t.Errorf("staff gid read as %#v, want 20", v)
	}

	users, err := dsclList(c, "/Users")
	if err != nil {
		t.Fatalf("dsclList(/Users): %v", err)
	}
	if len(users) < 2 {
		t.Errorf("dsclList(/Users) returned %d names", len(users))
	}

	// A missing account reads back as absent, not as an error.
	missing, err := macUserInfo(c, "halite-no-such-user-xyz")
	if err != nil {
		t.Fatalf("reading a missing account errored: %v", err)
	}
	if missing.Len() != 0 {
		t.Errorf("a missing account read as %v", missing)
	}

	// user.present in test mode: predicts, runs nothing.
	c.Test = true
	res, err := macUserPresentState(c, value.MapOf("name", "halite-no-such-user-xyz", "shell", "/bin/zsh"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil || !res.HasChanges() {
		t.Errorf("test-mode present reported %+v, want a prediction with changes", res)
	}
}

func mustAtoi(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("%q is not a number", s)
		}
		n = n*10 + int64(r-'0')
	}
	return n
}
