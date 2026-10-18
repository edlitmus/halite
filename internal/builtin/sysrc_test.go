package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// **A value sysrc would write as code is refused** (issue 321, M-45).
// rc.conf is sourced as root at boot and sysrc does not escape it: on
// beastie `bar=x"; echo pwned; "` printed "pwned" when the file was
// sourced, and `$(id)` ran id. None of these reaches sysrc; an ordinary
// setting still does.
func TestSysrcRefusesAValueThatWouldRunAsCode(t *testing.T) {
	for _, kv := range [][2]string{
		{"bar", `x"; echo pwned; "`},
		{"baz", "$(id)"},
		{"qux", "`id`"},
		{"foo", "a\nevil_enable=\"YES\""},
		{"-x", "1"},
		{"a b", "1"},
	} {
		rec := &exec.RecordingRunner{}
		c := newCtx(false)
		c.Runner = rec
		c.Lookup = func(string) string { return "/usr/sbin/sysrc" }
		// Called directly rather than through the registry, which runs
		// sysrc only on FreeBSD: the check is the same on every platform.
		if err := sysrcSet(c, kv[0], kv[1], ""); err == nil {
			t.Errorf("sysrc.set %s=%q was accepted", kv[0], kv[1])
		}
		res, err := sysrcPresent(c, value.MapOf("name", kv[0], "value", kv[1]))
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || *res.Result {
			t.Errorf("sysrc.managed %s=%q: %+v", kv[0], kv[1], res)
		}
		for _, cmd := range rec.RanCommands() {
			if strings.Contains(cmd, "=") {
				t.Errorf("%s=%q reached sysrc: %s", kv[0], kv[1], cmd)
			}
		}
	}
	rec := &exec.RecordingRunner{}
	c := newCtx(false)
	c.Runner = rec
	c.Lookup = func(string) string { return "/usr/sbin/sysrc" }
	if err := sysrcSet(c, "ifconfig_em0", "inet 10.0.0.5 netmask 255.255.255.0", ""); err != nil {
		t.Errorf("an ordinary setting was refused: %v", err)
	}
}

// **Only "unknown variable" means unset** (issue 321, section 5). Any
// non-zero exit from `sysrc -n` was read as "not set". On beastie (FreeBSD
// 15.1), an rc.conf with a syntax error answers every read with exit 1
// and "Syntax error: Unterminated quoted string" -- including for a
// variable the file does set -- so sysrc.get said it was unset and
// sysrc.absent said it was already absent, while the line stayed in the
// file. The outputs here are what sysrc printed there.
func TestSysrcTellsUnsetFromABrokenFile(t *testing.T) {
	ctx := func(res exec.Result) *exec.Context {
		c := newCtx(false)
		c.Runner = &exec.RecordingRunner{Default: res}
		c.Lookup = func(string) string { return "/usr/sbin/sysrc" }
		return c
	}
	unset := exec.Result{Code: 1, Stderr: "sysrc: unknown variable 'bar_enable'\n"}
	broken := exec.Result{Code: 1, Stderr: "/etc/rc.conf: 2: Syntax error: Unterminated quoted string\n"}

	if _, set, err := sysrcGet(ctx(unset), "bar_enable", ""); err != nil || set {
		t.Errorf("an unknown variable: set=%v err=%v", set, err)
	}
	_, set, err := sysrcGet(ctx(broken), "foo_enable", "")
	if err == nil {
		t.Fatalf("a file sysrc could not read was taken as foo_enable unset (set=%v)", set)
	}
	if !strings.Contains(err.Error(), "Syntax error") {
		t.Errorf("the error does not carry sysrc's reason: %v", err)
	}
	res, err := sysrcAbsent(ctx(broken), value.MapOf("name", "foo_enable"))
	if err == nil && res.Result != nil && *res.Result {
		t.Errorf("sysrc.absent on an unreadable file succeeded: %s", res.Comment)
	}
}
