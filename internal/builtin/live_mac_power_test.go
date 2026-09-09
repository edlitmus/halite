package builtin

import (
	"runtime"
	"testing"
)

// `mac_power` getters, read against the real `pmset` on this Mac.
//
// # Why it needs no gate
//
// Every getter runs `pmset -g custom`, which reads and changes nothing.
// The setters run `pmset -a`, which needs root and rewrites a real Mac's
// power policy, so they are not exercised here and no CI leg is a Mac —
// `evidence.go` records the module `assumed` for that reason.
//
// # What it establishes
//
// That `pmset -g custom` on a real machine has the shape the parser
// expects: an `AC Power` section every Mac has, the three sleep timers
// in it as `0`–`180` integers, and — for whichever of the optional
// flags this hardware reports — a value that reads back as a bool. A
// VM and an Apple Silicon Mac do not report `womp` or `autorestart`, so
// those are checked only when present rather than required.

func TestLiveMacPowerReadsThisMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_power is macOS's, and this is %s", runtime.GOOS)
	}
	c := realCtx(t)
	if c.Which("pmset") == "" {
		t.Skip("no `pmset` on this machine")
	}

	ac, err := macPowerReadSource(c, "ac")
	if err != nil {
		t.Fatalf("reading the AC power settings: %v", err)
	}
	if len(ac) == 0 {
		t.Fatal("`pmset -g custom` reported an empty AC section")
	}

	// The three sleep timers are on every Mac. Each getter must find its
	// label and return a 0..180 int64.
	for _, name := range []string{"computer_sleep", "display_sleep", "harddisk_sleep"} {
		got, err := macPowerGet(c, macPowerByName(name), "ac")
		if err != nil {
			t.Errorf("get_%s: %v", name, err)
			continue
		}
		n, ok := got.(int64)
		if !ok {
			t.Errorf("get_%s returned %T, want int64", name, got)
			continue
		}
		if n < 0 || n > 180 {
			t.Errorf("get_%s = %d, outside 0..180", name, n)
		}
	}

	// The flags are hardware-dependent. Check the ones this Mac reports;
	// a getter for one it does not must fail rather than invent a value.
	for _, name := range []string{"wake_on_network", "wake_on_modem", "restart_power_failure", "sleep_on_power_button"} {
		s := macPowerByName(name)
		_, present := ac[s.readLabel()]
		got, err := macPowerGet(c, s, "ac")
		if present {
			if err != nil {
				t.Errorf("get_%s: %q is in `pmset -g custom` but the getter failed: %v", name, s.readLabel(), err)
				continue
			}
			if _, ok := got.(bool); !ok {
				t.Errorf("get_%s returned %T, want bool", name, got)
			}
		} else if err == nil {
			t.Errorf("get_%s: %q is not in `pmset -g custom` but the getter returned %#v", name, s.readLabel(), got)
		}
	}

	// get_sleep bundles the three timers, and its keys are Salt's.
	m, err := macPowerGetSleep(c, "ac")
	if err != nil {
		t.Fatalf("get_sleep: %v", err)
	}
	for _, label := range []string{"Computer", "Display", "Hard Disk"} {
		if _, has := m.Get(label); !has {
			t.Errorf("get_sleep has no %q key: %v", label, m.StringKeys())
		}
	}
}
