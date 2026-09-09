package builtin

import (
	"runtime"
	"testing"
)

// mac_softwareupdate reads, against the real `softwareupdate` on this Mac.
//
// # Why it needs no gate, and what it leaves out
//
// `softwareupdate --schedule` (the read form) and the
// /Library/Updates index plist read and change nothing. `--list` is
// left out on purpose: it contacts Apple's update service, takes tens of
// seconds, and fails with no network — none of which belongs in the
// default suite. The install and download paths need root, reboot the
// machine, and no CI leg is a Mac, so they are not exercised;
// `evidence.go` records the module `assumed`.
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
