//go:build linux

package builtin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The shadow module, driven against the real chage, passwd and chpasswd.
//
// The unit tests hold the parser to lines captured from these tools. What
// they cannot hold is the other half of every function here: that the
// tool, handed the arguments this builds, writes the column the reader
// then reads. So this creates an account of its own, runs every mutating
// function against it through the registry (platform check and all), and
// asserts each column in `getent shadow`'s line -- split here with a bare
// strings.Split, not through parseShadowLine, so the module's reader is
// not also the judge of it.
//
// Safety, because this runs as root on a machine reached over SSH: only
// the account the test creates is ever named, and every other line of
// /etc/shadow is compared byte for byte before and after, so a function
// that touched root's line -- the key the session arrived by does not
// care, but a locked root is still a changed host -- fails the test
// rather than passing it.
func TestLiveShadowDrivesAThrowawayAccount(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates an account and changes its /etc/shadow line")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_SYSTEM_LIVE is set and this is not root; every shadow function reads or writes /etc/shadow")
	}
	for _, tool := range []string{"useradd", "userdel", "chage", "passwd", "chpasswd", "getent"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("this host has no %s", tool)
		}
	}

	suffix := make([]byte, 2)
	_, _ = rand.Read(suffix)
	name := "halite-shadow-" + hex.EncodeToString(suffix)
	others := shadowLinesExcept(t, name)
	t.Cleanup(func() {
		_ = exec.Command("userdel", name).Run()
		if after := shadowLinesExcept(t, name); after != others {
			t.Errorf("a line other than %s's changed in /etc/shadow", name)
		}
	})
	if out, err := exec.Command("useradd", "-M", name).CombinedOutput(); err != nil {
		t.Fatalf("useradd: %v: %s", err, out)
	}

	r := New()
	c := &hexec.Context{Runner: &hexec.OSRunner{}}
	call := func(fn string, kv ...any) any {
		t.Helper()
		out, err := r.Exec.Call(c, "shadow."+fn, value.MapOf(append([]any{"name", name}, kv...)...))
		if err != nil {
			t.Fatalf("shadow.%s: %v", fn, err)
		}
		return out
	}
	column := func(i int) string {
		t.Helper()
		out, err := exec.Command("getent", "shadow", name).Output()
		if err != nil {
			t.Fatalf("getent shadow %s: %v", name, err)
		}
		fields := strings.Split(strings.TrimSpace(string(out)), ":")
		if len(fields) < 9 {
			t.Fatalf("getent shadow %s printed %q", name, out)
		}
		return fields[i]
	}

	// info agrees with getent on a fresh account, whatever its family's
	// fresh hash is (`!!` on EL, `!` on Debian).
	info := call("info").(*value.Map)
	if v, _ := info.Get("passwd"); v != column(1) {
		t.Errorf("info passwd = %v, getent says %q", v, column(1))
	}
	users := call2(t, r, c, "shadow.list_users")
	if !containsAny(users, name) || !containsAny(users, "root") {
		t.Errorf("list_users = %v, want %s and root in it", users, name)
	}

	// Every day-count setter: one call that changes, then the same call
	// again, which must not need to (the read before acting decides it).
	for _, s := range []struct {
		fn, arg string
		val     any
		col     int
		want    string
	}{
		{"set_mindays", "mindays", int64(2), 3, "2"},
		{"set_maxdays", "maxdays", int64(90), 4, "90"},
		{"set_warndays", "warndays", int64(14), 5, "14"},
		{"set_inactdays", "inactdays", int64(30), 6, "30"},
		{"set_expire", "expire", "2027-01-31", 7, "20849"},
		{"set_date", "date", "2026-09-01", 2, "20697"},
		{"set_date", "date", int64(0), 2, "0"},
		{"set_expire", "expire", int64(-1), 7, ""},
		{"set_inactdays", "inactdays", int64(-1), 6, ""},
		{"set_maxdays", "maxdays", int64(-1), 4, ""},
	} {
		if got := call(s.fn, s.arg, s.val); got != true {
			t.Errorf("%s(%v) = %v", s.fn, s.val, got)
		}
		if got := column(s.col); got != s.want {
			t.Errorf("after %s(%v) column %d is %q, want %q", s.fn, s.val, s.col, got, s.want)
		}
		if got := call(s.fn, s.arg, s.val); got != true {
			t.Errorf("a second %s(%v) = %v", s.fn, s.val, got)
		}
		i := call("info").(*value.Map)
		key := map[int]string{2: "lstchg", 3: "min", 4: "max", 5: "warn", 6: "inact", 7: "expire"}[s.col]
		want := s.want
		if want == "" {
			want = "-1"
		}
		if v, _ := i.Get(key); fmt.Sprint(v) != want {
			t.Errorf("info %s = %v after %s(%v), want %s", key, v, s.fn, s.val, want)
		}
	}

	// Unlocking a field that is only the lock is refused by every tool,
	// and one of them (Debian's usermod -U) exits 0 doing it; the
	// function must be an error either way, and the field unchanged.
	lockOnly := column(1)
	if _, err := r.Exec.Call(c, "shadow.unlock_password", value.MapOf("name", name)); err == nil {
		t.Errorf("unlock_password on a lock-only field %q succeeded", lockOnly)
	}
	if column(1) != lockOnly {
		t.Errorf("a refused unlock changed the field from %q to %q", lockOnly, column(1))
	}

	// A hash, set, locked, unlocked, deleted.
	const hash = "$6$haliteshadow$QUzUz0ruJ3oEj2eAXSzmW9zsUeO3eOtHkMNrOyAYZvI9MPiimfG4fdq3mBTnJOkAOp.X0ncvp35uHNGCqeBrT/"
	if got := call("set_password", "password", hash); got != true {
		t.Errorf("set_password = %v", got)
	}
	if column(1) != hash {
		t.Fatalf("after set_password the field is %q", column(1))
	}
	if got := call("lock_password"); got != true || !strings.HasPrefix(column(1), "!") ||
		!strings.HasSuffix(column(1), hash) {
		t.Errorf("lock_password = %v, field %q", got, column(1))
	}
	locked := column(1)
	if got := call("lock_password"); got != true || column(1) != locked {
		t.Errorf("a second lock_password = %v and moved the field to %q", got, column(1))
	}
	if got := call("unlock_password"); got != true || column(1) != hash {
		t.Errorf("unlock_password = %v, field %q", got, column(1))
	}
	if got := call("del_password"); got != true || column(1) != "" {
		t.Errorf("del_password = %v, field %q", got, column(1))
	}

	// Test mode, against a field that would change: nothing moves.
	before := column(1) + ":" + column(3)
	tc := &hexec.Context{Runner: &hexec.OSRunner{}, Test: true}
	for fn, args := range map[string]*value.Map{
		"shadow.set_password":  value.MapOf("name", name, "password", hash),
		"shadow.lock_password": value.MapOf("name", name),
		"shadow.set_mindays":   value.MapOf("name", name, "mindays", int64(7)),
	} {
		if _, err := r.Exec.Call(tc, fn, args); err != nil {
			t.Errorf("test-mode %s: %v", fn, err)
		}
	}
	if after := column(1) + ":" + column(3); after != before {
		t.Errorf("test mode changed the line: %q -> %q", before, after)
	}
}

func call2(t *testing.T, r *Registries, c *hexec.Context, fn string) any {
	t.Helper()
	out, err := r.Exec.Call(c, fn, value.NewMap(0))
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return out
}

func containsAny(list any, want string) bool {
	items, _ := list.([]any)
	for _, i := range items {
		if i == want {
			return true
		}
	}
	return false
}

// shadowLinesExcept is /etc/shadow without one account's line, to show
// that nothing else moved.
func shadowLinesExcept(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("/etc/shadow")
	if err != nil {
		t.Fatalf("reading /etc/shadow: %v", err)
	}
	var keep []string
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, name+":") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}
