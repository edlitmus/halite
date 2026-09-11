package builtin

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerUdev installs the `udev` module of SPEC 15.3's Common Linux
// row.
//
// # Reading is udevadm's own export mode
//
// `udevadm info` prints a table for a person by default. `--export`
// (single device) and `--export-db` (every device) are udevadm's own
// machine-readable modes: shell-quoted `KEY='value'` for one device, or
// the same grouped under `P:`/`N:`/`U:`/`S:`/`E:` line prefixes for the
// whole database. Both are documented and stable, and neither is the
// column-aligned listing DIVERGENCE 5.31 was about.
//
// # `trigger` re-runs rules; it does not write them
//
// A udev rule is a text file in /etc/udev/rules.d, which `file.managed`
// already writes. What this module adds is the two things a rule
// change needs that a file write does not do by itself: `trigger`
// re-fires udev's rules against devices that are already present (a
// rule added after boot does nothing to a device udev already
// processed), and `reload_rules` tells the running daemon to re-read
// the rule files before the next event. Neither is idempotent in the
// sense a state expects — running a rule twice is what "trigger" means
// — which is why, like `modprobe`, there is no `udev` state; SPEC 15.5
// names none.
//
// # `settle` is a wait, not a mutation
//
// `udevadm settle` blocks until the event queue drains and changes
// nothing itself; it is here because a tree that just triggered a rule
// or hot-added a device needs to know when udev has caught up, the way
// a `cmd.run` might wait on a service.
func registerUdev(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "udev", Function: "version",
				Doc:       "Return the udev version.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return udevVersion(c) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "udev", Function: "info",
				Doc: "Return every property udev holds for one device.",
				Params: []signature.Param{
					opt("device", signature.Path, "", "The device node, such as /dev/sda1."),
					opt("syspath", signature.Path, "", "The sysfs path, such as /sys/class/block/sda1. One of device or syspath is required."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: udevInfoFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "udev", Function: "list",
				Doc: "Return every device udev knows about, with its node, subsystem and properties.",
				Params: []signature.Param{
					opt("subsystem", signature.String, "", "Limit to one subsystem, such as block or net. Empty lists them all."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: udevListFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "udev", Function: "trigger",
				Doc: "Re-run udev rules against devices that are already present.",
				Params: []signature.Param{
					choice("action", "change", "The event to simulate.", "add", "change", "remove", "bind", "unbind"),
					opt("subsystem", signature.String, "", "Limit to devices in this subsystem."),
					opt("sysname", signature.String, "", "Limit to devices whose kernel name matches this glob."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: udevTriggerFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "udev", Function: "settle",
				Doc: "Wait for udev's event queue to drain.",
				Params: []signature.Param{
					opt("timeout", signature.Int, int64(10), "Seconds to wait before giving up."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: udevSettleFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "udev", Function: "reload_rules",
				Doc:        "Tell the running udev daemon to re-read the rule files in /etc/udev/rules.d.",
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: udevReloadRulesFn,
		},
	)
}

func udevToolPresent(c *exec.Context) error {
	if c.Which("udevadm") == "" {
		return errors.New("this node has no `udevadm`; it is part of udev/systemd and every Linux system with device management has it")
	}
	return nil
}

func udevVersion(c *exec.Context) (any, error) {
	if err := udevToolPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: []string{"udevadm", "--version"}})
	if err != nil {
		return nil, fmt.Errorf("`udevadm --version` could not be run: %w", err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// udevUnquoteExport strips the single quotes `udevadm info -x` wraps
// each value in.
func udevUnquoteExport(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}

func udevInfoFn(c *exec.Context, args *value.Map) (any, error) {
	if err := udevToolPresent(c); err != nil {
		return nil, err
	}
	device := strings.TrimSpace(states.Str(args, "device", ""))
	syspath := strings.TrimSpace(states.Str(args, "syspath", ""))
	var argv []string
	switch {
	case device != "" && syspath != "":
		return nil, errors.New("give device or syspath, not both")
	case device != "":
		argv = []string{"udevadm", "info", "--query=property", "--export", "--name=" + device}
	case syspath != "":
		argv = []string{"udevadm", "info", "--query=property", "--export", "--path=" + syspath}
	default:
		return nil, errors.New("a device or a syspath must be named")
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("udevadm could not be run: %w", err)
	}
	if res.Code != 0 || strings.TrimSpace(res.Stdout) == "" {
		return nil, fmt.Errorf("udevadm has no device at %q", firstNonEmpty(device, syspath))
	}
	props := value.NewMap(16)
	for _, line := range strings.Split(res.Stdout, "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props.Set(key, udevUnquoteExport(val))
	}
	return props, nil
}

// udevDevice is one entry of `udevadm info --export-db`.
type udevDevice struct {
	Syspath    string
	Devnode    string
	Subsystem  string
	Symlinks   []string
	Properties map[string]string
}

// udevParseExportDB reads the whole-database export: devices separated
// by a blank line, each a run of `P:`/`N:`/`U:`/`S:`/`E:` lines. The
// other letters udevadm documents (M, R, T, D, I, L, Q) are not
// collected because nothing here reports them.
func udevParseExportDB(out string) []udevDevice {
	var (
		devices []udevDevice
		cur     *udevDevice
	)
	flush := func() {
		if cur != nil {
			devices = append(devices, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		prefix, rest, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		if cur == nil {
			cur = &udevDevice{Properties: map[string]string{}}
		}
		switch prefix {
		case "P":
			cur.Syspath = rest
		case "N":
			cur.Devnode = "/dev/" + rest
		case "U":
			cur.Subsystem = rest
		case "S":
			cur.Symlinks = append(cur.Symlinks, "/dev/"+rest)
		case "E":
			if k, v, ok := strings.Cut(rest, "="); ok {
				cur.Properties[k] = v
			}
		}
	}
	flush()
	return devices
}

func udevListFn(c *exec.Context, args *value.Map) (any, error) {
	if err := udevToolPresent(c); err != nil {
		return nil, err
	}
	subsystem := strings.TrimSpace(states.Str(args, "subsystem", ""))
	res, err := c.Run(exec.Command{Argv: []string{"udevadm", "info", "--export-db"}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("udevadm could not be run: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`udevadm info --export-db` exited %d: %s", res.Code, strings.TrimSpace(firstLine(res.Stderr)))
	}
	var out []any
	for _, d := range udevParseExportDB(res.Stdout) {
		if subsystem != "" && d.Subsystem != subsystem {
			continue
		}
		m := value.NewMap(4)
		m.Set("syspath", d.Syspath)
		m.Set("devnode", nilIfEmpty(d.Devnode))
		m.Set("subsystem", nilIfEmpty(d.Subsystem))
		links := make([]any, len(d.Symlinks))
		for i, s := range d.Symlinks {
			links[i] = s
		}
		m.Set("symlinks", links)
		props := value.NewMap(len(d.Properties))
		for k, v := range d.Properties {
			props.Set(k, v)
		}
		m.Set("properties", props)
		out = append(out, m)
	}
	return out, nil
}

func udevTriggerFn(c *exec.Context, args *value.Map) (any, error) {
	if err := udevToolPresent(c); err != nil {
		return nil, err
	}
	action := strings.TrimSpace(states.Str(args, "action", "change"))
	subsystem := strings.TrimSpace(states.Str(args, "subsystem", ""))
	sysname := strings.TrimSpace(states.Str(args, "sysname", ""))
	argv := []string{"udevadm", "trigger", "--action=" + action}
	if subsystem != "" {
		argv = append(argv, "--subsystem-match="+subsystem)
	}
	if sysname != "" {
		argv = append(argv, "--sysname-match="+sysname)
	}
	scope := "every matching device"
	if subsystem != "" || sysname != "" {
		parts := []string{}
		if subsystem != "" {
			parts = append(parts, "subsystem "+subsystem)
		}
		if sysname != "" {
			parts = append(parts, "name "+sysname)
		}
		scope = strings.Join(parts, ", ")
	}
	change := value.MapOf(scope, states.Change(nil, action))
	if c.Test {
		dry := append(append([]string{}, argv...), "--dry-run", "--verbose")
		res, err := c.Run(exec.Command{Argv: dry, IgnoreExitCode: true})
		count := 0
		if err == nil && res.Code == 0 {
			count = len(strings.Fields(strings.TrimSpace(res.Stdout)))
		}
		return udevMutateResult(c, true, fmt.Sprintf(
			"a %s event would be triggered for %s (%d device(s) match).", action, scope, count), change), nil
	}
	if err := udevRun(c, argv); err != nil {
		return nil, err
	}
	return udevMutateResult(c, true, fmt.Sprintf("a %s event was triggered for %s.", action, scope), change), nil
}

func udevSettleFn(c *exec.Context, args *value.Map) (any, error) {
	if err := udevToolPresent(c); err != nil {
		return nil, err
	}
	timeout := states.Int(args, "timeout", 10)
	if timeout <= 0 {
		return nil, fmt.Errorf("timeout is %d; it is a positive count of seconds", timeout)
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"udevadm", "settle", "--timeout=" + strconv.FormatInt(timeout, 10)},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, fmt.Errorf("udevadm could not be run: %w", err)
	}
	out := value.NewMap(1)
	out.Set("settled", res.Code == 0)
	return out, nil
}

func udevReloadRulesFn(c *exec.Context, args *value.Map) (any, error) {
	if err := udevToolPresent(c); err != nil {
		return nil, err
	}
	if c.Test {
		out := value.NewMap(2)
		out.Set("changed", true)
		out.Set("comment", "udev's rules would be reloaded. Nothing was changed: this was a test run.")
		return out, nil
	}
	if err := udevRun(c, []string{"udevadm", "control", "--reload"}); err != nil {
		return nil, err
	}
	out := value.NewMap(2)
	out.Set("changed", true)
	out.Set("comment", "udev's rules were reloaded.")
	return out, nil
}

func udevMutateResult(c *exec.Context, changed bool, comment string, change *value.Map) *value.Map {
	out := value.NewMap(3)
	out.Set("changed", changed)
	if c.Test && changed {
		out.Set("comment", comment+" Nothing was changed: this was a test run.")
	} else {
		out.Set("comment", comment)
	}
	if change != nil {
		out.Set("changes", change)
	}
	return out
}

func udevRun(c *exec.Context, argv []string) error {
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		return fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(), res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}
