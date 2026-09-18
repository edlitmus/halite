package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `softwareupdate --list` on macOS 26, with a mix of recommended and not.
const softwareUpdateListCurrent = `Software Update Tool

Finding available software
Software Update found the following new or updated software:
* Label: macOS Sequoia 15.6.1-24G90
	Title: macOS Sequoia 15.6.1, Version: 15.6.1, Size: 6819191KiB, Recommended: YES, Action: restart,
* Label: Command Line Tools for Xcode-16.4
	Title: Command Line Tools for Xcode, Version: 16.4, Size: 745661KiB, Recommended: NO, Action: ,
`

// The older bare-star form, without the "Label:" prefix.
const softwareUpdateListOld = `Software Update Tool

Software Update found the following new or updated software:
   * Security Update 2020-004
	Title: Security Update 2020-004, Version: 1.0, Size: 1234K, Recommended: YES, Action: restart,
`

func macSUCtx(t *testing.T, list string) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	runner := &exec.RecordingRunner{Responses: map[string]exec.Result{
		"softwareupdate --list": {Stdout: list},
	}}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "softwareupdate" {
			return "/usr/sbin/softwareupdate"
		}
		return ""
	}
	return c, runner
}

func TestParseSoftwareUpdateListBothForms(t *testing.T) {
	cur := parseSoftwareUpdateList(softwareUpdateListCurrent)
	if len(cur) != 2 {
		t.Fatalf("current form: read %d entries, want 2: %+v", len(cur), cur)
	}
	if cur[0].Label != "macOS Sequoia 15.6.1-24G90" || cur[0].Version != "15.6.1" || !cur[0].Recommended || cur[0].Action != "restart" {
		t.Errorf("entry 0 = %+v", cur[0])
	}
	if cur[1].Label != "Command Line Tools for Xcode-16.4" || cur[1].Recommended {
		t.Errorf("entry 1 = %+v", cur[1])
	}

	old := parseSoftwareUpdateList(softwareUpdateListOld)
	if len(old) != 1 || old[0].Label != "Security Update 2020-004" || !old[0].Recommended {
		t.Fatalf("old form parsed as %+v", old)
	}
}

func TestMacSoftwareUpdateListNoScanFlag(t *testing.T) {
	c, runner := macSUCtx(t, softwareUpdateListCurrent)
	runner.Responses["softwareupdate --list --no-scan"] = exec.Result{Stdout: softwareUpdateListCurrent}

	if _, err := macSoftwareUpdateList(c, true); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cmd := range runner.Ran {
		if len(cmd.Argv) == 3 && cmd.Argv[2] == "--no-scan" {
			found = true
		}
	}
	if !found {
		t.Errorf("no_scan did not add --no-scan: %v", runner.RanCommands())
	}
}

func TestMacSoftwareUpdateListErrorsOnRealFailure(t *testing.T) {
	c, runner := macSUCtx(t, "")
	runner.Responses["softwareupdate --list"] = exec.Result{
		Code: 1, Stderr: "No network connection.\n",
	}
	if _, err := macSoftwareUpdateList(c, false); err == nil {
		t.Error("a non-zero exit with no entries was not an error")
	}
}

func TestMacSoftwareUpdateScheduleEnabled(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{"Automatic checking for updates is turned on\n", true},
		{"Automatic checking for updates is turned off\n", false},
	} {
		runner := &exec.RecordingRunner{Responses: map[string]exec.Result{
			"softwareupdate --schedule": {Stdout: tc.out},
		}}
		c := newCtx(false)
		c.Runner = runner
		c.Lookup = func(string) string { return "/usr/sbin/softwareupdate" }
		got, err := macSoftwareUpdateScheduleEnabled(c)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("schedule %q read as %v, want %v", strings.TrimSpace(tc.out), got, tc.want)
		}
	}
}

func TestParseUpdatesIndex(t *testing.T) {
	empty := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>ProductPaths</key><dict/></dict></plist>`
	got, err := parseUpdatesIndex([]byte(empty))
	if err != nil || len(got) != 0 {
		t.Fatalf("empty index parsed as %v (%v)", got, err)
	}

	staged := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>ProductPaths</key><dict>
	<key>002-12345</key><string>/Library/Updates/002-12345</string>
	<key>001-99999</key><string>/Library/Updates/001-99999</string>
</dict></dict></plist>`
	got, err = parseUpdatesIndex([]byte(staged))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "001-99999" || got[1] != "002-12345" {
		t.Errorf("staged index parsed as %v", got)
	}
}

// The three functions macOS removed refuse by name, with the reason.
func TestMacSoftwareUpdateGoneFunctionsRefuse(t *testing.T) {
	c := newCtx(false)
	for _, fn := range []string{"ignore", "list_ignored", "reset_ignored"} {
		m := macSoftwareUpdateGoneModule(fn, "x")
		_, err := m.Fn(c, value.NewMap(0))
		if err == nil {
			t.Errorf("%s did not refuse", fn)
			continue
		}
		if !strings.Contains(err.Error(), "removed from macOS") || !strings.Contains(err.Error(), "MDM") {
			t.Errorf("%s refusal does not explain: %v", fn, err)
		}
	}
}

func TestMacSoftwareUpdateIsRegisteredAndRestricted(t *testing.T) {
	r := New()
	want := []string{
		"mac_softwareupdate.list_available", "mac_softwareupdate.update_available",
		"mac_softwareupdate.list_downloads", "mac_softwareupdate.download",
		"mac_softwareupdate.download_all", "mac_softwareupdate.update",
		"mac_softwareupdate.update_all", "mac_softwareupdate.schedule_enabled",
		"mac_softwareupdate.schedule_enable", "mac_softwareupdate.ignore",
		"mac_softwareupdate.list_ignored", "mac_softwareupdate.reset_ignored",
	}
	for _, name := range want {
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if len(sig.Platforms) != 1 || sig.Platforms[0] != "darwin" {
			t.Errorf("%s platforms = %v, want [darwin]", name, sig.Platforms)
		}
	}
}
