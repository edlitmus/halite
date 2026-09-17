package builtin

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// `mac_power`'s setters, driven against the real `pmset` on this Mac.
//
// # What makes this one different from the rest of the row
//
// There is nothing throwaway to write to. An account or a preference
// domain can be invented for a test and removed after; a Mac has one
// power policy and the setters write it. So this captures the machine's
// real configuration first, drives the setters, and puts every value
// back.
//
// **The restore does not go through the module.** `macPowerSet` writes
// with `pmset -a`, which sets *every* power source at once — so on a
// laptop whose AC and battery profiles differ, restoring through the
// module would leave both holding whichever value was captured last and
// silently destroy the difference. The cleanup writes each source
// separately with `pmset -c`, `-b` and `-u`, and only the sources
// `pmset -g custom` actually reported. This machine is a desktop and
// has AC alone, which is exactly the condition under which that bug
// would not have been noticed.
//
// # What it touches
//
// Three settings — `displaysleep`, `womp` and `powerbutton` — and the
// combined sleep-timer group, all restored. On a Mac that rejects
// `powerbutton` outright nothing is written for it and nothing needs
// restoring. Deliberately not touched:
// `sleep` on its own, because a low system-sleep timer can put the
// machine to sleep in the middle of the test. The combined group is
// driven with 0, which is "never", which cannot.
//
// # Why it skips without root
//
// `pmset -a` needs it. `HALITE_SYSTEM_LIVE=1` on top, because a
// `go test ./...` on a Mac somebody is working on should not rewrite
// its power policy even briefly. Run it:
//
//	sudo HALITE_SYSTEM_LIVE=1 go test -run TestLiveMacPowerSet -v ./internal/builtin/
//
// # What it establishes
//
// That a value written with `pmset -a` is the value `pmset -g custom`
// reports back through this module's own reader, per setting — and, for
// a setting this Mac reports but has no key to write, that the module
// says which of the two happened and changes nothing. `Sleep On Power
// Button` is the second kind on Apple silicon; it was the first kind on
// the Intel Macs this table was written from, which is why the test
// asks the machine rather than assuming either. DIVERGENCE 5.115.

// macPowerSourceFlags maps a `pmset -g custom` section to the flag that
// writes that source alone.
var macPowerSourceFlags = map[string]string{
	"AC Power:":      "-c",
	"Battery Power:": "-b",
	"UPS Power:":     "-u",
}

func macPowerLiveRoot(t *testing.T) (*exec.Context, map[string]map[string]string) {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to rewrite this Mac's power policy")
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_power is macOS's, and this is %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		t.Skip("pmset -a needs root; run it under sudo")
	}
	c := realCtx(t)
	if c.Which("pmset") == "" {
		t.Fatal("HALITE_SYSTEM_LIVE is set and there is no `pmset`; this is not a Mac")
	}

	res, err := c.Run(exec.Command{Argv: []string{"pmset", "-g", "custom"}, IgnoreExitCode: true})
	if err != nil {
		t.Fatalf("capturing the current policy: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("pmset -g custom exited %d: %s", res.Code, res.Stderr+res.Stdout)
	}
	sections := parsePmsetCustom(res.Stdout)
	if len(sections) == 0 {
		t.Fatal("`pmset -g custom` reported no power sources at all")
	}
	return c, sections
}

// restore puts one setting back on every source that reported it,
// writing each source on its own.
func macPowerRestore(t *testing.T, c *exec.Context, sections map[string]map[string]string, s macPowerSetting) {
	t.Helper()
	for section, vals := range sections {
		flag, ok := macPowerSourceFlags[section]
		if !ok {
			continue
		}
		was, had := vals[s.readLabel()]
		if !had {
			continue
		}
		res, err := c.Run(exec.Command{
			Argv:           []string{"pmset", flag, s.key, was},
			IgnoreExitCode: true,
		})
		if err == nil && res.Code != 0 && macPmsetRejectedTheKey(res.Stderr+res.Stdout) {
			// A key this Mac will not write is a key it never wrote, so
			// there is nothing to put back. Saying RESTORE FAILED here
			// would send somebody looking for damage that cannot exist.
			continue
		}
		if err != nil || res.Code != 0 {
			t.Errorf("RESTORE FAILED: pmset %s %s %s: %v %s",
				flag, s.key, was, err, res.Stderr+res.Stdout)
		}
	}
}

func TestLiveMacPowerSetWritesWhatItReads(t *testing.T) {
	c, original := macPowerLiveRoot(t)

	ac, ok := original["AC Power:"]
	if !ok {
		t.Fatal("no AC section; every Mac has one")
	}

	// A timer, a flag, and the flag whose written key is not the label
	// it is printed under — which is the one the module's own header
	// calls out and the one a fixture is most likely to get wrong.
	cases := []struct {
		setting string
		write   any
		want    any
	}{
		{"display_sleep", int64(17), int64(17)},
		{"wake_on_network", false, false},
		{"sleep_on_power_button", false, false},
	}

	for _, tc := range cases {
		s := macPowerByName(tc.setting)
		if _, reported := ac[s.readLabel()]; !reported {
			t.Logf("this Mac does not report %q; skipping %s", s.readLabel(), tc.setting)
			continue
		}
		t.Run(tc.setting, func(t *testing.T) {
			t.Cleanup(func() { macPowerRestore(t, c, original, s) })

			was, err := macPowerGet(c, s, "ac")
			if err != nil {
				t.Fatalf("reading %s before writing it: %v", tc.setting, err)
			}

			if err := macPowerSet(c, s, tc.write); err != nil {
				// A Mac can report a setting through `pmset -g custom`
				// and have no key to write it: `Sleep On Power Button`
				// on Apple silicon is one. That is the machine's answer,
				// not a defect, but the module has to say so clearly and
				// must not have changed anything.
				if !strings.Contains(err.Error(), "does not accept the key") {
					t.Fatalf("set_%s(%v): %v", tc.setting, tc.write, err)
				}
				t.Logf("this Mac does not take `pmset -a %s`: %v", s.key, err)
				if strings.Contains(err.Error(), "Usage: pmset") {
					t.Errorf("the refusal passes pmset's usage dump through: %v", err)
				}
				still, err := macPowerGet(c, s, "ac")
				if err != nil {
					t.Fatalf("reading %s after a refused write: %v", tc.setting, err)
				}
				if still != was {
					t.Errorf("%s was %#v before a write this Mac refused and is %#v after",
						tc.setting, was, still)
				}
				return
			}
			got, err := macPowerGet(c, s, "ac")
			if err != nil {
				t.Fatalf("get_%s: %v", tc.setting, err)
			}
			if got != tc.want {
				t.Errorf("set_%s wrote %v and get_%s read back %#v -- the key `pmset -a` "+
					"takes and the label `pmset -g custom` prints do not agree",
					tc.setting, tc.write, tc.setting, got)
			}

			// Writing the same value again is not an error, which is
			// what a tree calling this through `module.run` on every
			// highstate does.
			if err := macPowerSet(c, s, tc.write); err != nil {
				t.Errorf("writing %s a second time failed: %v", tc.setting, err)
			}
		})
	}
}

// The combined set_sleep/get_sleep pair, which writes three timers at
// once. Driven with 0 -- "never" -- because that is the one value that
// cannot put the machine to sleep while the test is running.
func TestLiveMacPowerSetSleepWritesEveryTimer(t *testing.T) {
	c, original := macPowerLiveRoot(t)

	t.Cleanup(func() {
		for _, timer := range macPowerSleepTimers {
			macPowerRestore(t, c, original, macPowerByName(timer.name))
		}
	})

	if err := macPowerSetSleep(c, int64(0)); err != nil {
		t.Fatalf("set_sleep(0): %v", err)
	}
	got, err := macPowerGetSleep(c, "ac")
	if err != nil {
		t.Fatalf("get_sleep: %v", err)
	}
	for _, timer := range macPowerSleepTimers {
		v, has := got.Get(timer.label)
		if !has {
			t.Errorf("get_sleep did not report %q", timer.label)
			continue
		}
		if v != int64(0) {
			t.Errorf("set_sleep(0) left %s at %#v", timer.label, v)
		}
	}
}

// A desktop has no battery section, and the reader falls back to AC
// rather than inventing an answer. Worth driving on hardware because
// the fallback is the kind of thing that reads as obviously correct and
// can still return the wrong map.
func TestLiveMacPowerMissingSourceFallsBackToAC(t *testing.T) {
	c, original := macPowerLiveRoot(t)

	for _, section := range []string{"Battery Power:", "UPS Power:"} {
		if _, present := original[section]; present {
			t.Skipf("this Mac reports %q, so there is no fallback to observe", section)
		}
	}

	s := macPowerByName("display_sleep")
	onAC, err := macPowerGet(c, s, "ac")
	if err != nil {
		t.Fatalf("reading ac: %v", err)
	}
	for _, source := range []string{"battery", "ups"} {
		got, err := macPowerGet(c, s, source)
		if err != nil {
			t.Errorf("reading %s on a Mac that has none: %v", source, err)
			continue
		}
		if got != onAC {
			t.Errorf("%s read back %#v where AC reads %#v; the fallback returned "+
				"something other than the AC section", source, got, onAC)
		}
	}
}
