package builtin

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `snap`, against a real snapd.
//
// # What this exists to settle
//
// `evidence.go` recorded `snap` as `Assumed` in these words: "nothing
// here has run against a real snapd, and the `snap list` fixtures were
// written from its documented columns rather than captured". That is the
// shape DIVERGENCE 5.31 is about, and it cost the same way again --
// `snap list` truncates a long channel with U+2026 and `--unicode=never`
// does not stop it, which the documentation does not say and a captured
// fixture shows immediately.
//
// # Nothing here installs or removes anything
//
// Every check below reads. Installing a snap on a node pulls from the
// store, takes a squashfs mount and a service, and removing one can take
// data with it; none of that belongs in a test that runs unattended
// against whatever machine is to hand. The mutating half of this module
// is still unwatched and `evidence.go` says so -- what this converts
// from assumed to captured is the reading, which is where the defect was.
func snapLive(t *testing.T) *exec.Context {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("snapd is Linux's, and this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to read this node's real snapd")
	}
	c := realCtx(t)
	// snapd is Ubuntu's by default and absent nearly everywhere else,
	// so a node without it is skipped by name rather than failed: this
	// is not a tool a distribution "should" have.
	if c.Which("snap") == "" {
		t.Skipf("this node has no `snap`; os_family is %q", liveOSFamily(t))
	}
	return c
}

// **The real table parses**, and every row it holds reads back.
func TestLiveSnapReadsTheRealList(t *testing.T) {
	c := snapLive(t)
	r := New()

	out, err := r.Exec.Call(c, "snap.list", value.NewMap(0))
	if err != nil {
		t.Fatalf("snap.list: %v", err)
	}
	listed, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("snap.list answered a %T", out)
	}
	if listed.Len() == 0 {
		t.Skip("this node has snapd and no snaps installed; there is no table to parse")
	}

	// Every row's every column, rather than `snapd`'s.
	//
	// This used to name `snapd`, on the stated grounds that "snapd itself
	// is a snap on every machine that has snapd, which makes it the one
	// row that can be named without installing anything". That is not
	// true, and the machine that disproved it is the one this test now
	// runs on: `ubuntu-24.04` installs snapd as a **deb**, so `snap list`
	// there is empty until something is installed, and a machine carrying
	// only a `core`-based snap lists `core` and that snap and no `snapd`
	// at all. On such a host the assertion failed for a host that is
	// perfectly well -- it was a fixture written from how snapd usually
	// looks rather than from what it guarantees.
	//
	// Checking every row is also strictly stronger: the header-driven
	// split is what can lose a column, and one named row exercised it
	// once. DIVERGENCE 5.151.
	for _, name := range listed.StringKeys() {
		raw, _ := listed.Get(name)
		row, ok := raw.(*value.Map)
		if !ok {
			t.Errorf("%s is a %T rather than a row", name, raw)
			continue
		}
		for _, field := range []string{"version", "revision", "channel", "publisher", "notes"} {
			v, _ := row.GetString(field)
			if s, _ := v.(string); strings.TrimSpace(s) == "" {
				t.Errorf("%s's %s is empty; the header-driven split lost a column: %v",
					name, field, row)
			}
		}
	}

	// **No channel comes back truncated.** This is the defect this file
	// was written for: `snap list` shortens a long channel with U+2026,
	// `--unicode=never` does not stop it, and a truncated value compared
	// against a declared one never matches -- so `snap.installed` would
	// refresh on every run and report a change every time.
	for _, name := range listed.StringKeys() {
		v, _ := listed.Get(name)
		ch, _ := v.(*value.Map).GetString("channel")
		s, _ := ch.(string)
		if snapChannelTruncated(s) {
			t.Errorf("%s reports channel %q, which is a prefix rather than a channel; "+
				"it should have been resolved through `snap info`", name, s)
		}
		if strings.TrimSpace(s) == "" {
			t.Errorf("%s reports no channel at all", name)
		}
	}
	t.Logf("%d snaps, channels: %v", listed.Len(), snapChannelsOf(listed))
}

// The resolved channel agrees with what snapd itself says.
//
// Read against `snap info` directly rather than against this build's own
// reader, so that the two cannot agree by sharing a mistake.
func TestLiveSnapChannelAgreesWithSnapInfo(t *testing.T) {
	c := snapLive(t)
	r := New()

	out, err := r.Exec.Call(c, "snap.list", value.NewMap(0))
	if err != nil {
		t.Fatalf("snap.list: %v", err)
	}
	listed, _ := out.(*value.Map)
	if listed == nil || listed.Len() == 0 {
		t.Skip("no snaps installed on this node")
	}

	for _, name := range listed.StringKeys() {
		v, _ := listed.Get(name)
		ch, _ := v.(*value.Map).GetString("channel")
		reported, _ := ch.(string)

		res, err := c.Run(exec.Command{
			Argv:           []string{"snap", "info", "--color=never", "--unicode=never", name},
			IgnoreExitCode: true,
		})
		if err != nil || res.Code != 0 {
			t.Logf("%s: snap info could not be read, skipping that one", name)
			continue
		}
		var tracking string
		for _, line := range strings.Split(res.Stdout, "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "tracking:"); ok {
				tracking = strings.TrimSpace(rest)
				break
			}
		}
		if tracking == "" {
			continue
		}
		if reported != tracking {
			t.Errorf("%s: snap.list reports channel %q and `snap info` says %q",
				name, reported, tracking)
		}
	}
}

func snapChannelsOf(listed *value.Map) []string {
	var out []string
	for _, name := range listed.StringKeys() {
		v, _ := listed.Get(name)
		ch, _ := v.(*value.Map).GetString("channel")
		s, _ := ch.(string)
		out = append(out, name+"="+s)
	}
	return out
}
