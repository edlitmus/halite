package builtin

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// mac_power, SPEC section 15.3's macOS row.
//
// It drives `pmset(8)`, the only supported interface to the power
// management settings a Mac keeps per power source. SPEC 15.5 names no
// `mac_power` state — only `mac_defaults` from this row — so this ships
// as execution functions and a tree that wants to converge a power
// setting reaches them through `module.run`. That is the same decision
// `win_registry` records, and not one to reverse in passing: an estate
// coming from Salt's `mac_power` state will want the same, and it is
// worth a person's attention rather than a quiet addition here.
//
// **The function names are Salt's**, getter and setter per setting, so a
// tree calling `mac_power.set_display_sleep` keeps working. Each pair is
// one row of `macPowerSettings` below; the reader and the writer are
// shared, so the pair cannot drift.
//
// **Reading is from `pmset -g custom`, not `pmset -g`.** `-g` prints the
// settings *in use* right now, which a caffeinate assertion or a live
// override changes; `-g custom` prints what is *configured* per power
// source, which is what a state converges against. The setter writes
// with `pmset -a` — every power source — matching Salt; a getter takes a
// `power_source` so a laptop whose AC and battery profiles differ can be
// asked about either.
//
// **One label is not its key.** `pmset` writes `powerbutton` and prints
// it back as `Sleep On Power Button`. The table carries both.
func registerMacPower(r *Registries) {
	source := choice("power_source", "ac",
		"Which power source's configured value to read: ac, battery or ups. "+
			"Setters always write every source, as `pmset -a` does.",
		"ac", "battery", "ups")

	for _, s := range macPowerSettings {
		s := s
		minutes := s.timer

		getDoc := s.getDoc
		if getDoc == "" {
			getDoc = "Read the configured " + s.human + "."
		}
		r.Exec.Add(exec.Module{
			Sig: signature.Signature{
				Module: "mac_power", Function: "get_" + s.name,
				Doc:       getDoc,
				Params:    []signature.Param{source},
				Returns:   s.returns,
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return macPowerGet(c, s, states.Str(args, "power_source", "ac"))
			},
		})

		var valParam signature.Param
		if minutes {
			valParam = req("minutes", signature.Any,
				"Minutes before it takes effect, 0 to 180. 0, `Never` or `Off` disables it.")
		} else {
			valParam = req("enabled", signature.Any,
				"on or off. `true`/`false`, `yes`/`no` and `1`/`0` are also accepted.")
		}
		setDoc := s.setDoc
		if setDoc == "" {
			setDoc = "Set the " + s.human + " for every power source."
		}
		r.Exec.Add(exec.Module{
			Sig: signature.Signature{
				Module: "mac_power", Function: "set_" + s.name,
				Doc:        setDoc,
				Params:     []signature.Param{valParam},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				raw, _ := args.Get("minutes")
				if !minutes {
					raw, _ = args.Get("enabled")
				}
				// macPowerSet validates the value and then honours test
				// mode, so a bad argument fails at --test.
				if err := macPowerSet(c, s, raw); err != nil {
					return nil, err
				}
				return true, nil
			},
		})
	}

	// Salt's combined pair: the three sleep timers together.
	r.Exec.Add(exec.Module{
		Sig: signature.Signature{
			Module: "mac_power", Function: "get_sleep",
			Doc:       "Read the computer, display and hard-disk sleep timers together.",
			Params:    []signature.Param{source},
			Returns:   "a mapping of Computer, Display and Hard Disk to their minute values",
			TestMode:  signature.TestNotApplicable,
			Platforms: macOnly,
			Section:   "15.3",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			return macPowerGetSleep(c, states.Str(args, "power_source", "ac"))
		},
	})
	r.Exec.Add(exec.Module{
		Sig: signature.Signature{
			Module: "mac_power", Function: "set_sleep",
			Doc: "Set the computer, display and hard-disk sleep timers to the same value, " +
				"for every power source.",
			Params: []signature.Param{req("minutes", signature.Any,
				"Minutes before sleep, 0 to 180. 0, `Never` or `Off` disables it.")},
			Mutates:    true,
			TestMode:   signature.TestReliable,
			Privileges: []string{"root"},
			Platforms:  macOnly,
			Section:    "15.3",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			raw, _ := args.Get("minutes")
			if err := macPowerSetSleep(c, raw); err != nil {
				return nil, err
			}
			return true, nil
		},
	})
}

// macPowerSleepTimers is the set Salt's combined get_sleep/set_sleep act
// on, with the labels get_sleep returns them under.
var macPowerSleepTimers = []struct{ label, name string }{
	{"Computer", "computer_sleep"},
	{"Display", "display_sleep"},
	{"Hard Disk", "harddisk_sleep"},
}

func macPowerGetSleep(c *exec.Context, source string) (*value.Map, error) {
	out := value.NewMap(len(macPowerSleepTimers))
	for _, t := range macPowerSleepTimers {
		v, err := macPowerGet(c, macPowerByName(t.name), source)
		if err != nil {
			return nil, err
		}
		out.Set(t.label, v)
	}
	return out, nil
}

func macPowerSetSleep(c *exec.Context, raw any) error {
	for _, t := range macPowerSleepTimers {
		if err := macPowerSet(c, macPowerByName(t.name), raw); err != nil {
			return err
		}
	}
	return nil
}

// macPowerSetting is one getter/setter pair.
type macPowerSetting struct {
	name    string // the Salt function suffix: set_<name> / get_<name>
	key     string // the token `pmset -a` takes
	label   string // how `pmset -g custom` prints it, when not equal to key
	human   string // for the generated doc sentence
	timer   bool   // a minute value rather than a 0/1 flag
	getDoc  string
	setDoc  string
	returns string
}

func (s macPowerSetting) readLabel() string {
	if s.label != "" {
		return s.label
	}
	return s.key
}

var macPowerSettings = []macPowerSetting{
	{name: "computer_sleep", key: "sleep", human: "system sleep timer", timer: true,
		returns: "the minutes before system sleep, or 0 when disabled"},
	{name: "display_sleep", key: "displaysleep", human: "display sleep timer", timer: true,
		returns: "the minutes before display sleep, or 0 when disabled"},
	{name: "harddisk_sleep", key: "disksleep", human: "hard-disk sleep timer", timer: true,
		returns: "the minutes before the disks spin down, or 0 when disabled"},
	{name: "wake_on_network", key: "womp",
		human: "wake-on-network-access setting", returns: "true when the Mac wakes for a magic packet"},
	{name: "wake_on_modem", key: "ring",
		human: "wake-on-modem-ring setting", returns: "true when the Mac wakes on a modem ring",
		getDoc: "Read the configured wake-on-modem-ring setting. Not every Mac reports this.",
		setDoc: "Set wake on modem ring for every power source. Not every Mac has a modem to ring."},
	{name: "restart_power_failure", key: "autorestart",
		human:   "restart-after-power-failure setting",
		returns: "true when the Mac restarts automatically after losing power"},
	{name: "sleep_on_power_button", key: "powerbutton", label: "Sleep On Power Button",
		human:   "sleep-on-power-button setting",
		returns: "true when pressing the power button sleeps the Mac"},
}

func macPowerByName(name string) macPowerSetting {
	for _, s := range macPowerSettings {
		if s.name == name {
			return s
		}
	}
	panic("mac_power: no setting named " + name)
}

func macPowerBin(c *exec.Context) string { return c.Which("pmset") }

func macPowerRequire(c *exec.Context) error {
	if macPowerBin(c) == "" {
		return fmt.Errorf("mac_power: `pmset` was not found; this build's macOS module needs it")
	}
	return nil
}

// macPowerReadSource parses `pmset -g custom` and returns the settings of
// one power source as a label->value map.
func macPowerReadSource(c *exec.Context, source string) (map[string]string, error) {
	if err := macPowerRequire(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: []string{"pmset", "-g", "custom"}})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("pmset -g custom: %s", firstLine(res.Stderr+res.Stdout))
	}

	want, ok := map[string]string{
		"ac": "AC Power:", "battery": "Battery Power:", "ups": "UPS Power:",
	}[source]
	if !ok {
		return nil, fmt.Errorf("mac_power: unknown power source %q", source)
	}

	sections := parsePmsetCustom(res.Stdout)
	vals, ok := sections[want]
	if !ok {
		// A desktop has no battery section, a machine without a UPS has
		// no UPS section. Fall back to AC, which every Mac has, rather
		// than inventing an answer.
		if source != "ac" {
			if ac, has := sections["AC Power:"]; has {
				return ac, nil
			}
		}
		return nil, fmt.Errorf("mac_power: `pmset -g custom` reports no %q section on this Mac", want)
	}
	return vals, nil
}

// parsePmsetCustom splits the `pmset -g custom` output into its power
// source sections. Each section is a header line ending in a colon
// followed by indented "` label   value`" lines whose value is the last
// whitespace-separated field.
func parsePmsetCustom(out string) map[string]map[string]string {
	sections := map[string]map[string]string{}
	var cur map[string]string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			cur = map[string]string{}
			sections[strings.TrimSpace(line)] = cur
			continue
		}
		if cur == nil {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val := fields[len(fields)-1]
		key := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), val))
		cur[key] = val
	}
	return sections
}

// macPowerGet reads one setting and returns it typed: an int64 for a
// timer, a bool for a flag.
func macPowerGet(c *exec.Context, s macPowerSetting, source string) (any, error) {
	vals, err := macPowerReadSource(c, source)
	if err != nil {
		return nil, err
	}
	raw, ok := vals[s.readLabel()]
	if !ok {
		return nil, fmt.Errorf("mac_power.get_%s: `pmset` does not report %q on this Mac", s.name, s.readLabel())
	}
	if s.timer {
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("mac_power.get_%s: %q is not a number", s.name, raw)
		}
		return n, nil
	}
	return strings.TrimSpace(raw) != "0", nil
}

// macPowerSet writes one setting for every power source. It honours test
// mode, so the combined set_sleep loop is test-safe as a whole and not
// only at the call site.
func macPowerSet(c *exec.Context, s macPowerSetting, raw any) error {
	if err := macPowerRequire(c); err != nil {
		return err
	}
	// Render the value first, so a bad argument is a failure even under
	// --test rather than a surprise on the real run.
	arg, err := macPowerRender(s, raw)
	if err != nil {
		return fmt.Errorf("mac_power.set_%s: %v", s.name, err)
	}
	if c.Test {
		return nil
	}
	res, err := c.Run(exec.Command{Argv: []string{"pmset", "-a", s.key, arg}})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("pmset -a %s %s: %s", s.key, arg, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// macPowerRender turns a setter argument into the token `pmset -a` takes:
// a 0-to-180 minute count for a timer, or "0"/"1" for a flag.
func macPowerRender(s macPowerSetting, raw any) (string, error) {
	if s.timer {
		n, err := macPowerTimer(raw)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(n), nil
	}
	on, err := macPowerBool(raw)
	if err != nil {
		return "", err
	}
	if on {
		return "1", nil
	}
	return "0", nil
}

// macPowerTimer coerces a sleep-timer argument: an integer 0 to 180, or
// `Never`/`Off` for 0.
func macPowerTimer(v any) (int, error) {
	switch t := v.(type) {
	case int64:
		return checkMinutes(int(t))
	case int:
		return checkMinutes(t)
	case float64:
		return checkMinutes(int(t))
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		if s == "never" || s == "off" || s == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number of minutes", t)
		}
		return checkMinutes(n)
	default:
		return 0, fmt.Errorf("a sleep timer is a number of minutes, not %s", value.TypeName(v))
	}
}

func checkMinutes(n int) (int, error) {
	if n < 0 || n > 180 {
		return 0, fmt.Errorf("a sleep timer is 0 to 180 minutes, got %d", n)
	}
	return n, nil
}

// macPowerBool coerces a flag argument. `value.Truthy` is not it: it
// reads any non-empty string as true, so `off` would enable the setting.
func macPowerBool(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case int64:
		return t != 0, nil
	case int:
		return t != 0, nil
	case float64:
		return t != 0, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "on", "yes", "true", "1", "enabled", "enable":
			return true, nil
		case "off", "no", "false", "0", "disabled", "disable":
			return false, nil
		}
		return false, fmt.Errorf("%q is not on or off", t)
	default:
		return false, fmt.Errorf("expected on or off, got %s", value.TypeName(v))
	}
}
