package builtin

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// mac_softwareupdate reads, against the real `softwareupdate` on this Mac.
//
// # Why it needs no gate, and what it leaves out
//
// `softwareupdate --schedule` (the read form) and the
// /Library/Updates index plist read and change nothing. `--list` is
// left out on purpose: it contacts Apple's update service, takes tens of
// seconds, and fails with no network — none of which belongs in the
// default suite. The download path needs root and the network, so it
// is the gated test below, run as root on the `macos` leg of
// `fleet.yml`. The install path is a registered refusal and nothing
// drives it. (This paragraph said once that no CI leg was a Mac and
// that `evidence.go` recorded the module `assumed`; both stopped being
// true when the `macos` leg demonstrated the download, and the table
// in `evidence.go` is the authority for the level, not this comment.)
//
// # What it establishes
//
// That `softwareupdate --schedule` prints a line this build can read a
// boolean out of, and that `list_downloads` reads the real
// /Library/Updates index without erroring.

func TestLiveMacSoftwareUpdateReadsThisMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_softwareupdate is macOS's, and this is %s", runtime.GOOS)
	}
	c := realCtx(t)
	if c.Which("softwareupdate") == "" {
		t.Skip("no `softwareupdate` on this machine")
	}

	// The schedule read works on every macOS and returns a clean bool.
	if _, err := macSoftwareUpdateScheduleEnabled(c); err != nil {
		t.Errorf("schedule_enabled against real softwareupdate: %v", err)
	}

	// The downloaded-updates index reads without error, empty or not.
	downloads, err := macSoftwareUpdateDownloads()
	if err != nil {
		t.Errorf("list_downloads against /Library/Updates: %v", err)
	}
	for _, d := range downloads {
		if _, ok := d.(string); !ok {
			t.Errorf("a download label is %T, not a string", d)
		}
	}
}

// The download path, against Apple's real update service.
//
// # Why this is the one gated test in this file
//
// Everything above reads. This fetches a payload over the network from
// Apple, which is slow, needs the machine to be online, and leaves the
// update sitting in /Library/Updates afterwards. It is the module's
// whole mutating surface -- `update` and `update_all` are registered
// refusals, because installing restarts the machine and this project
// cannot demonstrate that on any Mac it has -- so it is also the only
// thing standing between `mac_softwareupdate` and the release gate.
//
// # What it does not do
//
// It does not install, and it asserts that it did not: `--download`
// with a label fetches the payload and leaves the machine on the
// version it started on. That is the distinction the module is built
// around, and asserting it is the difference between demonstrating the
// download path and demonstrating that *something* ran.
//
// # Why it skips rather than fails when there is nothing to download
//
// A machine with no update available cannot exercise this, and that is
// a property of the machine rather than of the module. The skip says
// which case it hit, so a green run that proved nothing is not
// mistaken for a green run that proved something -- and `list_available`
// is the module's own reader, so a skip here is still a real answer
// from the code under test.
//
// # Why it prefers an update that is not macOS itself
//
// It took the first label on offer, and in November 2026 that became
// "macOS 27.2-26B123", the major upgrade, on GitHub's macos-15 runner.
// Downloading that sometimes stops to ask for a volume owner's
// password, which nothing here has, and the same label on the same
// image went through on a re-run: two PR merges were blocked one
// evening on a coin toss (DIVERGENCE 5.653). So the test now ranks
// what is on offer -- a non-OS update first (Safari's was the label the
// module was first demonstrated on), then an OS update within this
// Mac's major version, and a major upgrade only when that is all there
// is; macSoftwareUpdatePickDownload says why in that order.
//
// When the download does ask, that is the module's
// errMacSoftwareUpdateNeedsOwner, and the test skips naming it, after
// checking two things a skip must not hide: that the refusal is the
// clear error rather than whatever the tool printed first, and that the
// machine is still on the version it started on. Any other failure
// still fails.
func TestLiveMacSoftwareUpdateDownloadsWithoutInstalling(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_softwareupdate is macOS's, and this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to fetch a real update payload from Apple")
	}
	if os.Geteuid() != 0 {
		t.Skip("`softwareupdate --download` needs root; run it under sudo")
	}
	c := realCtx(t)
	if c.Which("softwareupdate") == "" {
		t.Skip("no `softwareupdate` on this machine")
	}

	// `--list` contacts Apple and takes tens of seconds, which is why
	// the reading test above leaves it out. Here it is the subject:
	// there is nothing to download without it.
	available, err := macSoftwareUpdateList(c, false)
	if err != nil {
		t.Fatalf("list_available against the real softwareupdate: %v", err)
	}
	if len(available) == 0 {
		t.Skip("this Mac reports no available updates, so there is no payload to fetch; " +
			"the download path stays undemonstrated on this machine")
	}

	for _, u := range available {
		t.Logf("on offer: label %q, title %q, version %q, recommended %v, action %q",
			u.Label, u.Title, u.Version, u.Recommended, u.Action)
	}

	before := macProductVersion(t, c)

	target := macSoftwareUpdatePickDownload(available, before)
	t.Logf("downloading %q from Apple", target.Label)
	r := New()
	out, err := r.Exec.Call(c, "mac_softwareupdate.download", value.MapOf("name", target.Label))
	if errors.Is(err, errMacSoftwareUpdateNeedsOwner) {
		if after := macProductVersion(t, c); after != before {
			t.Fatalf("the machine moved from %s to %s on a refused download", before, after)
		}
		t.Skipf("softwareupdate asked for an owner's password to download %q, the best update on "+
			"offer here by this test's ranking, and the module refused it clearly (%v); the payload "+
			"was not fetched, so the download path is undemonstrated on this run", target.Label, err)
	}
	if err != nil {
		t.Fatalf("download %q: %v", target.Label, err)
	}
	if out != true {
		t.Errorf("download reported %#v rather than true", out)
	}

	// The claim that matters: a download is not an install.
	if after := macProductVersion(t, c); after != before {
		t.Errorf("the machine moved from %s to %s; `--download` installed something", before, after)
	}
}

// macSoftwareUpdatePickDownload is the update the live test fetches, in
// order of preference: one that is not macOS itself; then an OS update
// within the major version this Mac runs; and only then a major
// upgrade, the kind that has been seen to ask for a password.
//
// The ranking is what was on offer, not a theory of it. On the macos-15
// runner the failing pick was "macOS 27.2-26B123", a major upgrade from
// 15.8.2; on 2026-11-11 the same image offered "macOS Sequoia
// 15.8.3-24H241" and "macOS 27.2.1-26B131", and nothing that was not an
// OS update, so "not macOS" alone would have chosen nothing better. The
// minor update downloaded with no prompt that day. An OS update is told
// by its label or title starting "macOS"; its major version is the part
// of `Version` before the first dot, compared with `sw_vers`'s.
func macSoftwareUpdatePickDownload(available []softwareUpdateEntry, running string) softwareUpdateEntry {
	major := func(v string) string { m, _, _ := strings.Cut(v, "."); return m }
	isOS := func(u softwareUpdateEntry) bool {
		return strings.HasPrefix(u.Label, "macOS") || strings.HasPrefix(u.Title, "macOS")
	}
	for _, u := range available {
		if !isOS(u) {
			return u
		}
	}
	for _, u := range available {
		if major(u.Version) == major(running) {
			return u
		}
	}
	return available[0]
}

// macProductVersion is what `sw_vers` says this Mac is running, so the
// test can show the download left it there.
func macProductVersion(t *testing.T, c *exec.Context) string {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: []string{"sw_vers", "-productVersion"}, IgnoreExitCode: true})
	if err != nil || res.Code != 0 {
		t.Fatalf("sw_vers: %v (%s)", err, strings.TrimSpace(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout)
}
