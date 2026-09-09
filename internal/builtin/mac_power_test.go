package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// `pmset -g custom` on a laptop: two power sources, and they differ.
// Captured shape from a real Mac, with a Battery section added so the
// per-source split is exercised.
const pmsetCustomFixture = `AC Power:
 Sleep On Power Button 1
 autorestartatconnect 0
 standby              0
 ttyskeepawake        1
 powernap             1
 displaysleep         20
 womp                 1
 networkoversleep     0
 sleep                0
 tcpkeepalive         1
 autorestart          1
 disksleep            10
Battery Power:
 Sleep On Power Button 1
 standby              1
 ttyskeepawake        1
 powernap             0
 displaysleep         2
 womp                 0
 sleep                5
 autorestart          0
 disksleep            10
`

// macPowerCtx gives a context whose `pmset -g custom` returns the fixture
// and which believes pmset is installed. The tests call the module's own
// helpers rather than r.Exec.Call, because the signature is darwin-only
// and Call refuses it on the Linux and Windows CI runners.
func macPowerCtx(t *testing.T, custom string) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	runner := &exec.RecordingRunner{Responses: map[string]exec.Result{
		"pmset -g custom": {Stdout: custom},
	}}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "pmset" {
			return "/usr/bin/pmset"
		}
		return ""
	}
	return c, runner
}

func TestPmsetCustomIsSplitByPowerSource(t *testing.T) {
	sections := parsePmsetCustom(pmsetCustomFixture)
	if len(sections) != 2 {
		t.Fatalf("read %d sections, want 2: %v", len(sections), sections)
	}
	ac := sections["AC Power:"]
	if ac["displaysleep"] != "20" || ac["disksleep"] != "10" || ac["sleep"] != "0" {
		t.Errorf("AC section read as %v", ac)
	}
	// The label with spaces in it is one key, and its value is the last field.
	if ac["Sleep On Power Button"] != "1" {
		t.Errorf("AC 'Sleep On Power Button' read as %q", ac["Sleep On Power Button"])
	}
	batt := sections["Battery Power:"]
	if batt["displaysleep"] != "2" || batt["sleep"] != "5" || batt["womp"] != "0" {
		t.Errorf("Battery section read as %v", batt)
	}
}

func TestMacPowerGettersReadTheRightSourceAndType(t *testing.T) {
	c, _ := macPowerCtx(t, pmsetCustomFixture)

	cases := []struct {
		name   string
		source string
		want   any
	}{
		{"display_sleep", "ac", int64(20)},
		{"display_sleep", "battery", int64(2)},
		{"computer_sleep", "ac", int64(0)},
		{"harddisk_sleep", "ac", int64(10)},
		{"wake_on_network", "ac", true},
		{"wake_on_network", "battery", false},
		{"restart_power_failure", "battery", false},
		{"sleep_on_power_button", "ac", true},
	}
	for _, tc := range cases {
		got, err := macPowerGet(c, macPowerByName(tc.name), tc.source)
		if err != nil {
			t.Errorf("get_%s(%s): %v", tc.name, tc.source, err)
			continue
		}
		if got != tc.want {
			t.Errorf("get_%s(%s) = %#v, want %#v", tc.name, tc.source, got, tc.want)
		}
	}
}

func TestMacPowerGetSleepReturnsTheThreeTimers(t *testing.T) {
	c, _ := macPowerCtx(t, pmsetCustomFixture)
	m, err := macPowerGetSleep(c, "battery")
	if err != nil {
		t.Fatal(err)
	}
	for label, want := range map[string]int64{"Computer": 5, "Display": 2, "Hard Disk": 10} {
		if v, _ := m.Get(label); v != want {
			t.Errorf("get_sleep[%q] = %#v, want %d", label, v, want)
		}
	}
}

// A setting pmset does not report on this Mac is an error naming it, not
// a false zero.
func TestMacPowerGetMissingSettingErrors(t *testing.T) {
	c, _ := macPowerCtx(t, pmsetCustomFixture) // fixture has no `ring`
	_, err := macPowerGet(c, macPowerByName("wake_on_modem"), "ac")
	if err == nil {
		t.Fatal("get_wake_on_modem on a Mac that does not report `ring` returned no error")
	}
	if !strings.Contains(err.Error(), "ring") {
		t.Errorf("the error does not name the setting: %v", err)
	}
}

// A desktop has no Battery section; asking for it falls back to AC
// rather than failing.
func TestMacPowerBatteryFallsBackToACOnADesktop(t *testing.T) {
	desktop := "AC Power:\n displaysleep         30\n sleep                0\n"
	c, _ := macPowerCtx(t, desktop)
	got, err := macPowerGet(c, macPowerByName("display_sleep"), "battery")
	if err != nil {
		t.Fatalf("desktop battery read: %v", err)
	}
	if got != int64(30) {
		t.Errorf("desktop battery read = %#v, want the AC value 30", got)
	}
}

func TestMacPowerSettersRunPmsetWithTheRightKey(t *testing.T) {
	cases := []struct {
		name  string
		arg   any
		key   string
		value string
	}{
		{"display_sleep", int64(15), "displaysleep", "15"},
		{"computer_sleep", "Never", "sleep", "0"},
		{"harddisk_sleep", "0", "disksleep", "0"},
		{"wake_on_network", "on", "womp", "1"},
		{"wake_on_network", false, "womp", "0"},
		{"restart_power_failure", "yes", "autorestart", "1"},
		{"sleep_on_power_button", "off", "powerbutton", "0"},
	}
	for _, tc := range cases {
		c, runner := macPowerCtx(t, pmsetCustomFixture)
		if err := macPowerSet(c, macPowerByName(tc.name), tc.arg); err != nil {
			t.Errorf("set_%s(%v): %v", tc.name, tc.arg, err)
			continue
		}
		want := "pmset -a " + tc.key + " " + tc.value
		found := false
		for _, ran := range runner.RanCommands() {
			if ran == want {
				found = true
			}
		}
		if !found {
			t.Errorf("set_%s(%v) ran %v, want %q", tc.name, tc.arg, runner.RanCommands(), want)
		}
	}
}

func TestMacPowerSetSleepWritesAllThreeTimers(t *testing.T) {
	c, runner := macPowerCtx(t, pmsetCustomFixture)
	if err := macPowerSetSleep(c, int64(45)); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sleep", "displaysleep", "disksleep"} {
		want := "pmset -a " + key + " 45"
		found := false
		for _, ran := range runner.RanCommands() {
			if ran == want {
				found = true
			}
		}
		if !found {
			t.Errorf("set_sleep did not run %q: %v", want, runner.RanCommands())
		}
	}
}

func TestMacPowerSetRejectsBadValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  any
	}{
		{"display_sleep", int64(999)},
		{"display_sleep", "soon"},
		{"wake_on_network", "maybe"},
	} {
		c, runner := macPowerCtx(t, pmsetCustomFixture)
		if err := macPowerSet(c, macPowerByName(tc.name), tc.arg); err == nil {
			t.Errorf("set_%s(%v) was accepted", tc.name, tc.arg)
		}
		for _, ran := range runner.RanCommands() {
			if strings.HasPrefix(ran, "pmset -a") {
				t.Errorf("set_%s(%v) ran %q despite a bad value", tc.name, tc.arg, ran)
			}
		}
	}
}

// macPowerSet honours test mode: it validates the value and then runs
// nothing.
func TestMacPowerSetTestModeRunsNothing(t *testing.T) {
	c, runner := macPowerCtx(t, pmsetCustomFixture)
	c.Test = true
	if err := macPowerSet(c, macPowerByName("display_sleep"), int64(15)); err != nil {
		t.Fatal(err)
	}
	for _, ran := range runner.RanCommands() {
		if strings.HasPrefix(ran, "pmset -a") {
			t.Errorf("test mode ran %q", ran)
		}
	}
	// A bad value still fails at --test.
	if err := macPowerSet(c, macPowerByName("display_sleep"), int64(999)); err == nil {
		t.Error("a bad value was accepted in test mode")
	}
}

func TestMacPowerIsRegisteredAndRestricted(t *testing.T) {
	r := New()
	got := 0
	for _, name := range r.Exec.Signatures().Names() {
		if !strings.HasPrefix(name, "mac_power.") {
			continue
		}
		got++
		sig, _ := r.Exec.Signatures().Lookup(name)
		if len(sig.Platforms) != 1 || sig.Platforms[0] != "darwin" {
			t.Errorf("%s platforms = %v, want [darwin]", name, sig.Platforms)
		}
	}
	// 7 getter/setter pairs plus the combined get_sleep/set_sleep.
	if got != 16 {
		t.Errorf("mac_power registers %d functions, want 16", got)
	}
}
