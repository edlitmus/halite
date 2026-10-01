package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Every /etc/shadow line below was captured on 2026-09-30 from three lab
// hosts -- Rocky Linux 9.8 (shadow-utils 4.9-16.el9, passwd 0.80),
// AlmaLinux 8.10 (shadow-utils 4.6-23.el8_10, passwd 0.80) and Debian 13
// (passwd 1:4.17.4-2) -- for a throwaway account, `halite-shadow-cap1`,
// that the capture created and deleted. `getent shadow` printed the same
// bytes as the file on every host. The hash is a syntactically valid
// SHA-512 crypt string the capture made up; it is nobody's password.

const shadowCapturedHash = "$6$saltsaltsalt$XJ0x8m3wyIBvYRwWGuu/l/2QEGyLlJfT0T9tW2cDFJ6D9ZC3ucBFR9cMuZBZVTyx7C0aRYqArjLwlUpNr7mV1."

var shadowCaptured = map[string]string{
	// Straight after `useradd -M`: EL writes `!!`, Debian `!`.
	"el fresh":     "halite-shadow-cap1:!!:20726:0:99999:7:::",
	"debian fresh": "halite-shadow-cap1:!:20726:0:99999:7:::",
	// `chage -m 2 -M 90 -W 14 -I 30 -E 2027-01-31`, then `chage -d 2026-09-01` (all three).
	"el aged": "halite-shadow-cap1:!!:20697:2:90:14:30:20849:",
	// `chage -E -1`, `-I -1`, `-d -1`: the columns are emptied, not written as -1.
	"el cleared": "halite-shadow-cap1:!!::2:90:14:::",
	// `chage -d 0`, which is a zero, not an empty column.
	"el forced": "halite-shadow-cap1:!!:0:2:90:14:::",
	// A hash set with `chpasswd -e`, then `passwd -l` on EL and on Debian, then `usermod -L`.
	"el passwd -l":      "halite-shadow-cap1:!!" + shadowCapturedHash + ":20726:2:90:14:::",
	"debian passwd -l":  "halite-shadow-cap1:!" + shadowCapturedHash + ":20726:2:90:14:::",
	"usermod -L (both)": "halite-shadow-cap1:!" + shadowCapturedHash + ":20726:2:90:14:::",
	// `passwd -d` (all three).
	"deleted": "halite-shadow-cap1::20726:2:90:14:::",
}

func withShadowFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shadow")
	body := "root:*:20726:0:99999:7:::\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := shadowFile
	shadowFile = path
	t.Cleanup(func() { shadowFile = old })
	return path
}

func shadowCall(c *exec.Context, function string, args *value.Map) (any, error) {
	for _, m := range shadowExecModules() {
		if m.Sig.Function != function {
			continue
		}
		bound, errs := m.Sig.Bind(nil, args)
		if len(errs) > 0 {
			return nil, fmt.Errorf("shadow.%s: %v", function, errs)
		}
		return m.Fn(c, bound)
	}
	return nil, fmt.Errorf("shadow.%s is not a function", function)
}

func shadowCtx(test bool) (*exec.Context, *exec.RecordingRunner) {
	runner := &exec.RecordingRunner{}
	c := newCtx(test)
	c.Runner = runner
	c.Lookup = func(name string) string { return "/usr/bin/" + name }
	return c, runner
}

func TestShadowIsRegisteredForLinuxOnly(t *testing.T) {
	r := New()
	for _, m := range shadowExecModules() {
		if !r.Exec.Has("shadow." + m.Sig.Function) {
			t.Errorf("shadow.%s is not registered", m.Sig.Function)
		}
		if len(m.Sig.Platforms) != 1 || m.Sig.Platforms[0] != "linux" {
			t.Errorf("shadow.%s declares %v; FreeBSD's account database is not this module's (see shadow.go)",
				m.Sig.Function, m.Sig.Platforms)
		}
	}
}

func TestShadowInfoReadsEachCapturedLineAsSaltDoes(t *testing.T) {
	withShadowFile(t, shadowCaptured["el aged"])
	got, err := shadowInfo("halite-shadow-cap1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name": "halite-shadow-cap1", "passwd": "!!", "lstchg": int64(20697), "min": int64(2),
		"max": int64(90), "warn": int64(14), "inact": int64(30), "expire": int64(20849),
	}
	for k, v := range want {
		if g, _ := got.Get(k); g != v {
			t.Errorf("%s = %#v, want %#v", k, g, v)
		}
	}

	// An emptied column is -1, as in Salt, and a zero stays a zero:
	// `chage -d 0` means "change it at next login", `-d -1` means unset.
	withShadowFile(t, shadowCaptured["el cleared"])
	got, _ = shadowInfo("halite-shadow-cap1")
	for k, v := range map[string]int64{"lstchg": -1, "inact": -1, "expire": -1, "min": 2} {
		if g, _ := got.Get(k); g != v {
			t.Errorf("cleared: %s = %#v, want %d", k, g, v)
		}
	}
	withShadowFile(t, shadowCaptured["el forced"])
	got, _ = shadowInfo("halite-shadow-cap1")
	if g, _ := got.Get("lstchg"); g != int64(0) {
		t.Errorf("forced: lstchg = %#v, want 0", g)
	}
}

func TestShadowInfoForAMissingAccountIsSaltsEmptyMapping(t *testing.T) {
	withShadowFile(t, shadowCaptured["debian fresh"])
	got, err := shadowInfo("nobody-here")
	if err != nil {
		t.Fatal(err)
	}
	if got.Len() != 8 {
		t.Fatalf("keys = %v, want Salt's eight", got.Keys())
	}
	for _, k := range got.Keys() {
		if v, _ := got.Get(k); v != "" {
			t.Errorf("%s = %#v, want empty", k, v)
		}
	}
	// And a prefix of a real name is not that name.
	if got, _ := shadowInfo("halite-shadow"); got != nil {
		if v, _ := got.Get("name"); v != "" {
			t.Errorf("a prefix matched %q", v)
		}
	}
}

// The ageing reader user.present uses goes through the same parser, so
// the state and the module agree on every column by construction.
func TestUserPresentAgeingReadsTheSameColumnsAsShadowInfo(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("readAging refuses off Linux by design")
	}
	withShadowFile(t, shadowCaptured["el aged"])
	a, found, err := readAging("halite-shadow-cap1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if *a.Min != 2 || *a.Max != 90 || *a.Warn != 14 || *a.Inact != 30 || *a.Expire != 20849 {
		t.Errorf("readAging = %v %v %v %v %v", *a.Min, *a.Max, *a.Warn, *a.Inact, *a.Expire)
	}
}

func TestShadowDaysMatchesWhatChageStored(t *testing.T) {
	for in, want := range map[any]int64{
		"2027-01-31": 20849, // captured: `chage -E 2027-01-31` stored 20849
		"2026-09-01": 20697, // captured: `chage -d 2026-09-01` stored 20697
		"1970-01-02": 1,     // captured
		int64(20000): 20000, // captured: `chage -E 20000`
		int64(0):     0,
		int64(-1):    -1,
		"-1":         -1,
	} {
		got, err := shadowDays(in)
		if err != nil || got != want {
			t.Errorf("shadowDays(%#v) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []any{"not-a-date", int64(-2), "1969-12-31", "2027-02-30"} {
		if _, err := shadowDays(bad); err == nil {
			t.Errorf("shadowDays(%#v) was accepted", bad)
		}
	}
}

func TestShadowSetAttributeHandsChageTheDayNumber(t *testing.T) {
	withShadowFile(t, shadowCaptured["el fresh"])
	c, runner := shadowCtx(false)
	// The fake runner changes nothing, so the read-back disagrees and
	// the function must say so rather than trust chage's exit 0.
	_, err := shadowCall(c, "set_expire", value.MapOf("name", "halite-shadow-cap1", "expire", "2027-01-31"))
	if err == nil || !strings.Contains(err.Error(), "column reads -1") {
		t.Errorf("an unchanged column was not reported: %v", err)
	}
	if got := runner.RanCommands(); len(got) != 1 || got[0] != "chage -E 20849 halite-shadow-cap1" {
		t.Errorf("ran %q", got)
	}
}

func TestShadowSetAttributeIsANoOpWhenTheColumnMatches(t *testing.T) {
	withShadowFile(t, shadowCaptured["el aged"])
	c, runner := shadowCtx(false)
	for fn, args := range map[string]*value.Map{
		"set_mindays":   value.MapOf("name", "halite-shadow-cap1", "mindays", int64(2)),
		"set_maxdays":   value.MapOf("name", "halite-shadow-cap1", "maxdays", "90"),
		"set_warndays":  value.MapOf("name", "halite-shadow-cap1", "warndays", int64(14)),
		"set_inactdays": value.MapOf("name", "halite-shadow-cap1", "inactdays", int64(30)),
		"set_expire":    value.MapOf("name", "halite-shadow-cap1", "expire", "2027-01-31"),
		"set_date":      value.MapOf("name", "halite-shadow-cap1", "date", int64(20697)),
	} {
		got, err := shadowCall(c, fn, args)
		if err != nil || got != true {
			t.Errorf("%s = %v, %v; want true", fn, got, err)
		}
	}
	if len(runner.Ran) != 0 {
		t.Errorf("a converged account ran %q", runner.RanCommands())
	}
}

func TestShadowFunctionsAreFalseForAnAccountWithNoLine(t *testing.T) {
	withShadowFile(t, shadowCaptured["el fresh"])
	c, runner := shadowCtx(false)
	for fn, args := range map[string]*value.Map{
		"set_mindays":     value.MapOf("name", "ghost", "mindays", int64(2)),
		"lock_password":   value.MapOf("name", "ghost"),
		"unlock_password": value.MapOf("name", "ghost"),
		"del_password":    value.MapOf("name", "ghost"),
		"set_password":    value.MapOf("name", "ghost", "password", shadowCapturedHash),
	} {
		if got, err := shadowCall(c, fn, args); err != nil || got != false {
			t.Errorf("%s(ghost) = %v, %v; want false", fn, got, err)
		}
	}
	if len(runner.Ran) != 0 {
		t.Errorf("ran %q for an account that does not exist", runner.RanCommands())
	}
}

func TestShadowLockTreatsEitherFamilysPrefixAsLocked(t *testing.T) {
	for _, which := range []string{"el fresh", "debian fresh", "el passwd -l", "debian passwd -l", "usermod -L (both)"} {
		withShadowFile(t, shadowCaptured[which])
		c, runner := shadowCtx(false)
		got, err := shadowCall(c, "lock_password", value.MapOf("name", "halite-shadow-cap1"))
		if err != nil || got != true || len(runner.Ran) != 0 {
			t.Errorf("%s: lock_password = %v, %v, ran %q; want true with nothing run", which, got, err, runner.RanCommands())
		}
	}
}

// Debian's `usermod -U` refused to unlock a lock-only field and exited
// 0; EL's `passwd -u` exited 254 and Debian's 3. The column is the
// answer, so a tool that exits 0 and changes nothing is still a failure.
func TestShadowUnlockReadsTheColumnNotTheExitStatus(t *testing.T) {
	withShadowFile(t, shadowCaptured["debian fresh"])
	c, runner := shadowCtx(false)
	runner.Responses = map[string]exec.Result{
		"passwd -u halite-shadow-cap1": {Code: 0, Stderr: "passwd: unlocking the password would result in a passwordless account.\n"},
	}
	_, err := shadowCall(c, "unlock_password", value.MapOf("name", "halite-shadow-cap1"))
	if err == nil || !strings.Contains(err.Error(), "passwordless") {
		t.Errorf("a refusal with exit 0 was not reported: %v", err)
	}
	runner.Responses["passwd -u halite-shadow-cap1"] = exec.Result{Code: 254, Stderr: "passwd: Unsafe operation (use -f to force)\n"}
	if _, err := shadowCall(c, "unlock_password", value.MapOf("name", "halite-shadow-cap1")); err == nil ||
		!strings.Contains(err.Error(), "exited 254") {
		t.Errorf("EL's refusal was not carried: %v", err)
	}
}

func TestShadowTestModeRunsNothing(t *testing.T) {
	withShadowFile(t, shadowCaptured["el passwd -l"])
	c, runner := shadowCtx(true)
	for fn, args := range map[string]*value.Map{
		"unlock_password": value.MapOf("name", "halite-shadow-cap1"),
		"del_password":    value.MapOf("name", "halite-shadow-cap1"),
		"set_password":    value.MapOf("name", "halite-shadow-cap1", "password", "$6$x$y"),
		"set_maxdays":     value.MapOf("name", "halite-shadow-cap1", "maxdays", int64(30)),
	} {
		if got, err := shadowCall(c, fn, args); err != nil || got != true {
			t.Errorf("test mode %s = %v, %v", fn, got, err)
		}
	}
	if len(runner.Ran) != 0 {
		t.Errorf("test mode ran %q", runner.RanCommands())
	}
}

// chpasswd reads `name:hash` records one per line, so a hash with a line
// break in it would be a second record, for whichever account it named.
func TestShadowSetPasswordRefusesAHashThatWouldBeASecondRecord(t *testing.T) {
	withShadowFile(t, shadowCaptured["deleted"])
	c, runner := shadowCtx(false)
	for _, bad := range []string{"x\nroot:$6$evil", "a:b", "x\x00y", "x\ry"} {
		if _, err := shadowCall(c, "set_password", value.MapOf("name", "halite-shadow-cap1", "password", bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if len(runner.Ran) != 0 {
		t.Errorf("ran %q", runner.RanCommands())
	}
}

func TestShadowSetPasswordPassesTheHashOnStandardInput(t *testing.T) {
	withShadowFile(t, shadowCaptured["deleted"])
	c, runner := shadowCtx(false)
	_, _ = shadowCall(c, "set_password", value.MapOf("name", "halite-shadow-cap1", "password", shadowCapturedHash))
	if len(runner.Ran) != 1 {
		t.Fatalf("ran %q", runner.RanCommands())
	}
	cmd := runner.Ran[0]
	if strings.Join(cmd.Argv, " ") != "chpasswd -e" || cmd.Stdin != "halite-shadow-cap1:"+shadowCapturedHash+"\n" {
		t.Errorf("ran %q with stdin %q", cmd.Argv, cmd.Stdin)
	}
}

func TestShadowRefusesANameAToolWouldReadAsAnOption(t *testing.T) {
	withShadowFile(t, shadowCaptured["el fresh"])
	c, runner := shadowCtx(false)
	for _, bad := range []string{"-a", "--help", "a:b", "a b", "a\nb", ""} {
		if _, err := shadowCall(c, "lock_password", value.MapOf("name", bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if len(runner.Ran) != 0 {
		t.Errorf("ran %q", runner.RanCommands())
	}
}

func TestShadowListUsersIsSorted(t *testing.T) {
	withShadowFile(t, shadowCaptured["el fresh"], "abc:*:1:0:99999:7:::")
	names, err := readShadowNames()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "abc,halite-shadow-cap1,root" {
		t.Errorf("names = %q", names)
	}
}
