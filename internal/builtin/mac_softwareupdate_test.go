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

// Every refusal is reachable with the arguments a tree actually passes.
//
// # What this is guarding
//
// A refusal registers so that a tree carrying the name from Salt gets
// an explanation instead of "unknown function", which reads as a typo.
// That only works if the call reaches the refusal — and a signature
// declaring no parameters rejects `name=...` during argument
// validation, one layer above, with "argument \"name\" is not a
// parameter of this function". Which also reads as a typo.
//
// Calling `m.Fn` directly, as the test above does, cannot see this:
// `Fn` is past the validation. This goes through the registry, which is
// the path a tree takes.
func TestMacSoftwareUpdateRefusalsAreReachableAsATreeCallsThem(t *testing.T) {
	r := New()
	c := newCtx(false)

	cases := []struct {
		fn   string
		args *value.Map
		want string
	}{
		{"ignore", value.MapOf("name", "macOS Sequoia 15.6.1-24G90"), "removed from macOS"},
		{"list_ignored", value.NewMap(0), "removed from macOS"},
		{"reset_ignored", value.NewMap(0), "removed from macOS"},
		{"update", value.MapOf("name", "macOS Sequoia 15.6.1-24G90", "restart", true),
			"halite does not install"},
		{"update_all", value.MapOf("recommended", true, "restart", true),
			"halite does not install"},
	}

	for _, tc := range cases {
		name := "mac_softwareupdate." + tc.fn
		_, err := r.Exec.Call(c, name, tc.args)
		if err == nil {
			t.Errorf("%s did not refuse", name)
			continue
		}
		if strings.Contains(err.Error(), "is not a parameter of this function") {
			t.Errorf("%s rejected the arguments before reaching its refusal, so a tree "+
				"carrying it from Salt is told it made a typo: %v", name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: the refusal does not say %q: %v", name, tc.want, err)
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

// Installing is refused by name, and the refusal says whose decision it
// is.
//
// The distinction matters to whoever reads it: the `ignore` functions
// next door refuse because macOS took the option away, and nothing can
// give a tree what it asked for. `softwareupdate --install` works. This
// module declines to run it, and an operator who cannot tell those
// apart will go looking for a macOS release note that does not exist.
func TestMacSoftwareUpdateInstallRefusesAndSaysWhose(t *testing.T) {
	r := New()
	// `update` takes a label and `update_all` does not, the same split
	// Salt has.
	for fn, args := range map[string]*value.Map{
		"update":     value.MapOf("name", "macOS Sequoia 15.6.1-24G90"),
		"update_all": value.MapOf("recommended", true),
	} {
		name := "mac_softwareupdate." + fn
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered; a tree carrying it from Salt would get "+
				"`unknown function`, which reads as a typo", name)
		}
		if sig.Mutates {
			t.Errorf("%s refuses and still declares Mutates", name)
		}

		_, err := r.Exec.Call(newCtx(false), name, args)
		if err == nil {
			t.Fatalf("%s did not refuse", name)
		}
		for _, want := range []string{
			"halite does not install",
			"halite's decision, not a",
			"download",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not say %q: %v", fn, want, err)
			}
		}
	}
}

// Downloading still runs, and no longer takes the install path's
// `restart`.
func TestMacSoftwareUpdateDownloadRunsAndHasNoRestart(t *testing.T) {
	r := New()
	sig, ok := r.Exec.Signatures().Lookup("mac_softwareupdate.download")
	if !ok {
		t.Fatal("mac_softwareupdate.download is not registered")
	}
	if !sig.Mutates {
		t.Error("download fetches a payload and does not declare Mutates")
	}
	for _, p := range sig.Params {
		if p.Name == "restart" {
			t.Error("download still takes `restart`, which only ever meant anything " +
				"to the install path")
		}
	}

	runner := &exec.RecordingRunner{}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "softwareupdate" {
			return "/usr/sbin/softwareupdate"
		}
		return ""
	}
	if _, err := r.Exec.Call(c, "mac_softwareupdate.download",
		value.MapOf("name", "Security Update 2020-004")); err != nil {
		t.Fatalf("download: %v", err)
	}
	ran := runner.RanCommands()
	if len(ran) != 1 || !strings.Contains(ran[0], "--download") {
		t.Fatalf("download ran %v", ran)
	}
	if strings.Contains(ran[0], "--install") {
		t.Errorf("download ran an install: %s", ran[0])
	}
}
