package builtin

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerSystemModule installs the `system` module of SPEC 15.2.
//
// # This is the most dangerous file in the project
//
// Four of the functions below can take the machine they run on off the
// network for good: `halt`, `poweroff`, `shutdown`, `reboot`. Every one
// of them is built from a pure argument-vector function of `goos` and its
// own arguments -- the shape `quota.quotaSetArgv` established -- so that
// the exact command each platform would run is checkable from any host,
// including this one, without ever invoking it. Nothing in this file's
// own tests runs `halt`, `poweroff`, `reboot`, `shutdown` or `init`; test
// mode for the power verbs runs nothing at all, not even a read, because
// there is nothing here worth probing before predicting the answer.
//
// # What this module deliberately does not do
//
//   - **No hostname function.** `hostname.get_hostname`, `set_hostname`
//     and the rest already own that name, in hostname.go.
//   - **No uptime or clock reader.** `status.uptime` and `status.time`
//     (system.go) already answer "what time is it" and "how long has
//     this node been up"; this module only ever writes the clock, never
//     reads it back for a caller.
//   - **No `hwclock`.** It is a Linux-only concept -- the hardware RTC
//     as distinct from the kernel's clock -- and FreeBSD, half this
//     project's own fleet, has no such tool. `date` is the one thing
//     both platforms actually have, so it is the one thing this module
//     drives.
//
// # Two platforms, not the five `quota` covers
//
// `quota` groups linux, freebsd, openbsd, netbsd and dragonfly under one
// table because `edquota` is one BSD-lineage tool and a wrong flag is
// *refused* by it. A wrong flag to `shutdown(8)` is not refused, it is
// obeyed, on hardware that may not have a person standing in front of
// it. So this module claims only the two platforms it has real evidence
// for: Linux's `shutdown`/`halt`/`poweroff` are documented by systemd,
// and FreeBSD's usage line below was read from the real, setuid-root
// `/sbin/shutdown` on the machine this was written on --
//
//	usage: shutdown [-] [-c | -f | -h | -p | -r | -k] [-o [-n]] [-q] time [warning-message ...]
//
// -- and FreeBSD's `date` positional format was read the same way, from
// `/rescue/date`'s own usage line:
//
//	usage: date [-jnRu] [-I[date|hours|minutes|seconds|ns]] [-f input_fmt]
//	            [ -z output_zone ] [-r filename|seconds] [-v[+|-]val[y|m|w|d|H|M|S]]
//	            [[[[[[cc]yy]mm]dd]HH]MM[.SS] | new_date] [+output_fmt]
//
// OpenBSD, NetBSD and DragonFly are BSD-lineage too and probably share
// both, but "probably" is not the standard this file holds itself to;
// they are left for whoever next runs this against one of them.
//
// # `system.reboot`/`system.halt` and the `reboot` module
//
// reboot.go's own comment calls `system.reboot` and `system.halt` "the
// verbs that act now", and that is exactly what they are here: with no
// delay given, `system.reboot`'s time argument is `now`. What it adds
// over the bare `reboot` command is the same delay and message the
// `reboot` module's `schedule` function takes, because the underlying
// tool -- `shutdown(8)` -- takes them too and refusing to pass them
// through would be this module pretending the tool can do less than it
// can. What it does *not* add is a cancel window: that is `reboot`'s
// reason to exist, layered above this one, for a tree that wants time to
// be countermanded. An operator who wants it now reaches for this
// module; a tree that wants a chance to change its mind reaches for that
// one. Both end up running the same `shutdown(8)`, which is also why
// `reboot`'s own FreeBSD `/var/run/noshutdown` interlock is honoured
// here too, through the same `rebootBlocked` this package's `reboot.go`
// already defines -- one file's operator-level "not this machine" should
// stop both doors, not just the one it was written behind.
func registerSystemModule(r *Registries) {
	registerSystemPower(r)
	registerSystemClock(r)
	registerSystemComputerDesc(r)
}

// systemPlatforms is Linux and FreeBSD, and only those -- see the package
// comment above for why this module does not follow `quota`'s wider
// table.
var systemPlatforms = []string{"linux", "freebsd"}

// ---- the power verbs ----

func registerSystemPower(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "system", Function: "halt",
				Doc: "Halt this machine immediately. This stops it without cutting power; " +
					"see `system.poweroff` for that.",
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  systemPlatforms,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return systemPowerRun(c, "halt", 0, "")
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "system", Function: "poweroff",
				Doc:        "Power this machine off immediately.",
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  systemPlatforms,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return systemPowerRun(c, "poweroff", 0, "")
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "system", Function: "shutdown",
				Doc: "Power this machine off, now or after a delay, telling anyone logged in why. " +
					"This is the generic bring-it-down verb driven through `shutdown(8)`; " +
					"`system.halt` and `system.poweroff` are their own immediate commands.",
				Params: []signature.Param{
					opt("delay", signature.Int, int64(0), "Minutes from now. 0 means immediately."),
					opt("message", signature.String, "", "A message broadcast to anyone logged in. Empty sends none."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  systemPlatforms,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return systemPowerRun(c, "shutdown", states.Int(args, "delay", 0), states.Str(args, "message", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "system", Function: "reboot",
				Doc: "Reboot this machine, now or after a delay, telling anyone logged in why. " +
					"With no delay this acts now; `reboot.schedule` is the version with a " +
					"cancel window for an unattended tree.",
				Params: []signature.Param{
					opt("delay", signature.Int, int64(0), "Minutes from now. 0 means immediately."),
					opt("message", signature.String, "", "A message broadcast to anyone logged in. Empty sends none."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  systemPlatforms,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return systemPowerRun(c, "reboot", states.Int(args, "delay", 0), states.Str(args, "message", ""))
			},
		},
	)
}

// systemPowerVerbPastTense is how each verb reads in a comment once it
// has run, or would have.
var systemPowerVerbPastTense = map[string]string{
	"halt":     "halted",
	"poweroff": "powered off",
	"reboot":   "rebooted",
	"shutdown": "shut down",
}

// systemPowerTimeArg spells `shutdown(8)`'s time argument, which both
// platforms this module supports write the same way: `now`, or `+N`
// minutes from now. Neither takes an absolute clock time here, since a
// relative delay is unambiguous and an absolute one would need to agree
// with the caller about a timezone this function has no way to ask about.
func systemPowerTimeArg(delayMinutes int64) string {
	if delayMinutes <= 0 {
		return "now"
	}
	return "+" + strconv.FormatInt(delayMinutes, 10)
}

// systemPowerArgv is the command one verb runs on one platform. It is a
// pure function of its arguments, so every row is checkable from any
// host: the fix plan.md drew out of `quota`'s own two fixtures, each of
// which had forced a branch its platform does not take.
//
// `halt` and `poweroff` take the standalone commands of the same name on
// both platforms, and take no delay or message: neither Linux's
// util-linux `halt`/`poweroff` nor FreeBSD's `/sbin/halt` family accepts
// a scheduled time. Anything that needs one is one of the other two
// verbs.
//
// `reboot` is `shutdown -r`, the same flag on both platforms.
//
// `shutdown` -- the generic "bring it down" verb -- differs by one
// letter, and the letters are not interchangeable between platforms.
// systemd's own manual says `-h` is "equivalent to --poweroff, unless
// --halt is specified also", so `shutdown -h` on Linux already means
// power off, which is the universal `shutdown -h now` idiom every admin
// types. FreeBSD's `-h` instead means "halt the system instead of
// rebooting", with no promise of cutting power; its `-p` is the one that
// turns the power off after halting, per the usage line captured in this
// file's package comment. So `-h` and `-p` are how each platform spells
// the same salt-level meaning, not a divergence between them.
func systemPowerArgv(goos, verb string, delayMinutes int64, message string) ([]string, error) {
	switch goos {
	case "linux", "freebsd":
	default:
		return nil, fmt.Errorf("this build does not know how to %s %s", verb, goos)
	}
	message = strings.TrimSpace(message)
	switch verb {
	case "halt":
		return []string{"halt"}, nil
	case "poweroff":
		return []string{"poweroff"}, nil
	case "reboot":
		argv := []string{"shutdown", "-r", systemPowerTimeArg(delayMinutes)}
		if message != "" {
			argv = append(argv, message)
		}
		return argv, nil
	case "shutdown":
		flag := "-h"
		if goos == "freebsd" {
			flag = "-p"
		}
		argv := []string{"shutdown", flag, systemPowerTimeArg(delayMinutes)}
		if message != "" {
			argv = append(argv, message)
		}
		return argv, nil
	}
	return nil, fmt.Errorf("%q is not halt, poweroff, reboot or shutdown", verb)
}

// systemPowerRun drives one verb: it validates, builds the command,
// honours test mode, and runs it.
//
// **Test mode calls `c.Run` zero times.** There is no "would this
// change anything" question to answer here the way there is for
// `quota.set` -- the machine is always in a state a halt, a poweroff, a
// reboot or a shutdown changes -- so nothing is worth reading first, and
// the strongest guarantee this file can give a nervous reader is that
// the test-mode branch below never reaches `c.Run` at all.
func systemPowerRun(c *exec.Context, verb string, delayMinutes int64, message string) (any, error) {
	if delayMinutes < 0 {
		return nil, fmt.Errorf("delay is %d minutes; it is 0 (now) or a positive count of minutes", delayMinutes)
	}
	argv, err := systemPowerArgv(runtime.GOOS, verb, delayMinutes, message)
	if err != nil {
		return nil, err
	}
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`, which is how %s does this", argv[0], runtime.GOOS)
	}
	// `reboot` and `shutdown` go through `shutdown(8)`, which is exactly
	// what `reboot.schedule` uses, and FreeBSD's copy of that tool
	// refuses outright while /var/run/noshutdown exists. `halt` and
	// `poweroff` are their own standalone commands and bypass it
	// entirely -- which is a real difference between the verbs, not an
	// oversight, so it is checked here rather than for every verb.
	if argv[0] == "shutdown" {
		if why := rebootBlocked(); why != "" {
			return nil, errors.New(why)
		}
	}
	verbed := systemPowerVerbPastTense[verb]
	if c.Test {
		out := value.NewMap(3)
		out.Set("changed", true)
		out.Set("comment", fmt.Sprintf(
			"this machine would be %s. Nothing was changed: this was a test run.", verbed))
		out.Set("command", exec.Command{Argv: argv}.String())
		return out, nil
	}
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return nil, fmt.Errorf("%s: %w", argv[0], err)
	}
	out := value.NewMap(2)
	out.Set("changed", true)
	out.Set("comment", fmt.Sprintf("this machine was told to be %s.", verbed))
	return out, nil
}

// ---- the system clock ----
//
// Reading the clock is `status.time`, in system.go, and stays there:
// this file only ever writes it.

func registerSystemClock(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "system", Function: "set_system_time",
				Doc: "Set this node's clock to a time of day, keeping today's date. " +
					"`status.time` is how to read the clock; this only ever writes it.",
				Params: []signature.Param{
					req("hour", signature.Int, "0 to 23."),
					req("minute", signature.Int, "0 to 59."),
					opt("second", signature.Int, int64(0), "0 to 59."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  systemPlatforms,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				target, err := systemComposeTime(time.Now(),
					states.Int(args, "hour", 0), states.Int(args, "minute", 0), states.Int(args, "second", 0))
				if err != nil {
					return nil, err
				}
				return systemApplyClock(c, target)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "system", Function: "set_system_date",
				Doc: "Set this node's clock to a calendar date, keeping the time of day. " +
					"`status.time` is how to read the clock; this only ever writes it.",
				Params: []signature.Param{
					req("year", signature.Int, "Four digits."),
					req("month", signature.Int, "1 to 12."),
					req("day", signature.Int, "1 to 31."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  systemPlatforms,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				target, err := systemComposeDate(time.Now(),
					states.Int(args, "year", 0), states.Int(args, "month", 0), states.Int(args, "day", 0))
				if err != nil {
					return nil, err
				}
				return systemApplyClock(c, target)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "system", Function: "set_system_date_time",
				Doc: "Set this node's clock to a calendar date and a time of day together. " +
					"`status.time` is how to read the clock; this only ever writes it.",
				Params: []signature.Param{
					req("year", signature.Int, "Four digits."),
					req("month", signature.Int, "1 to 12."),
					req("day", signature.Int, "1 to 31."),
					req("hour", signature.Int, "0 to 23."),
					req("minute", signature.Int, "0 to 59."),
					opt("second", signature.Int, int64(0), "0 to 59."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  systemPlatforms,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				target, err := systemComposeDateTime(
					states.Int(args, "year", 0), states.Int(args, "month", 0), states.Int(args, "day", 0),
					states.Int(args, "hour", 0), states.Int(args, "minute", 0), states.Int(args, "second", 0))
				if err != nil {
					return nil, err
				}
				return systemApplyClock(c, target)
			},
		},
	)
}

// systemValidateTimeOfDay checks the three fields every clock-setting
// function that touches the time of day takes.
func systemValidateTimeOfDay(hour, minute, second int64) error {
	if hour < 0 || hour > 23 {
		return fmt.Errorf("hour is %d; it is 0 to 23", hour)
	}
	if minute < 0 || minute > 59 {
		return fmt.Errorf("minute is %d; it is 0 to 59", minute)
	}
	if second < 0 || second > 59 {
		return fmt.Errorf("second is %d; it is 0 to 59", second)
	}
	return nil
}

// systemValidateCalendarDate checks the three fields every clock-setting
// function that touches the date takes, and returns the month as a
// `time.Month` for the caller's convenience.
func systemValidateCalendarDate(year, month, day int64) (time.Month, error) {
	if year < 1970 || year > 9999 {
		return 0, fmt.Errorf("year is %d; it is 1970 to 9999", year)
	}
	if month < 1 || month > 12 {
		return 0, fmt.Errorf("month is %d; it is 1 to 12", month)
	}
	if day < 1 || day > 31 {
		return 0, fmt.Errorf("day is %d; it is 1 to 31", day)
	}
	return time.Month(month), nil
}

// systemComposeTime overlays a time of day onto today's date.
func systemComposeTime(now time.Time, hour, minute, second int64) (time.Time, error) {
	if err := systemValidateTimeOfDay(hour, minute, second); err != nil {
		return time.Time{}, err
	}
	return time.Date(now.Year(), now.Month(), now.Day(), int(hour), int(minute), int(second), 0, now.Location()), nil
}

// systemComposeDate overlays a calendar date onto the current time of
// day.
//
// `time.Date` normalises an out-of-range day rather than rejecting it --
// February 30th becomes March 2nd -- and silently acting on the
// normalised date would set the clock to a day nobody asked for. The
// result is checked against what was asked for and refused if they
// disagree.
func systemComposeDate(now time.Time, year, month, day int64) (time.Time, error) {
	m, err := systemValidateCalendarDate(year, month, day)
	if err != nil {
		return time.Time{}, err
	}
	t := time.Date(int(year), m, int(day), now.Hour(), now.Minute(), now.Second(), 0, now.Location())
	if t.Year() != int(year) || t.Month() != m || t.Day() != int(day) {
		return time.Time{}, fmt.Errorf("%04d-%02d-%02d is not a real date", year, month, day)
	}
	return t, nil
}

// systemComposeDateTime is systemComposeDate and systemComposeTime
// together, for the function that takes both at once.
func systemComposeDateTime(year, month, day, hour, minute, second int64) (time.Time, error) {
	if err := systemValidateTimeOfDay(hour, minute, second); err != nil {
		return time.Time{}, err
	}
	m, err := systemValidateCalendarDate(year, month, day)
	if err != nil {
		return time.Time{}, err
	}
	t := time.Date(int(year), m, int(day), int(hour), int(minute), int(second), 0, time.Local)
	if t.Year() != int(year) || t.Month() != m || t.Day() != int(day) {
		return time.Time{}, fmt.Errorf("%04d-%02d-%02d is not a real date", year, month, day)
	}
	return t, nil
}

// systemClockArgv is the command that sets the clock to a given time.
//
// The two platforms this module supports do not just take different
// flags for the same format, as `quota`'s two tools do -- they take
// different *formats*. GNU `date`'s own --help (run on this project's
// Linux compatibility layer while writing this) documents `-s`/`--set`
// taking a free-form string, so this writes an unambiguous
// `YYYY-MM-DD HH:MM:SS`. FreeBSD's `date` has no such flag; its own
// usage line, read from `/rescue/date` on the real host, gives the
// setting form as positional digits in the order century, year, month,
// day, hour, minute, and an optional `.second` --
// `[[[[[[cc]yy]mm]dd]HH]MM[.SS]]` -- which is a different field order
// from GNU's own legacy positional form (`MMDDhhmm[[CC]YY][.ss]`, month
// and day first). Neither format is used for the other platform.
func systemClockArgv(goos string, t time.Time) ([]string, error) {
	switch goos {
	case "linux":
		return []string{"date", "--set=" + t.Format("2006-01-02 15:04:05")}, nil
	case "freebsd":
		return []string{"date", t.Format("200601021504.05")}, nil
	}
	return nil, fmt.Errorf("this build does not know how to set the clock on %s", goos)
}

// systemApplyClock runs systemClockArgv's command, honouring test mode.
func systemApplyClock(c *exec.Context, target time.Time) (any, error) {
	argv, err := systemClockArgv(runtime.GOOS, target)
	if err != nil {
		return nil, err
	}
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`, which is how %s sets its clock", argv[0], runtime.GOOS)
	}
	formatted := target.Format(time.RFC3339)
	if c.Test {
		out := value.NewMap(3)
		out.Set("changed", true)
		out.Set("comment", fmt.Sprintf(
			"the clock would be set to %s. Nothing was changed: this was a test run.", formatted))
		out.Set("command", exec.Command{Argv: argv}.String())
		return out, nil
	}
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return nil, fmt.Errorf("%s: %w", argv[0], err)
	}
	out := value.NewMap(2)
	out.Set("changed", true)
	out.Set("comment", fmt.Sprintf("the clock was set to %s.", formatted))
	return out, nil
}

// ---- the computer description ----

// EtcMachineInfoPath is where systemd keeps PRETTY_HOSTNAME, as a
// variable so a test can point it somewhere harmless -- the same
// convention hostname.go's EtcHostnamePath uses.
var EtcMachineInfoPath = "/etc/machine-info"

// registerSystemComputerDesc installs `get_computer_desc` and
// `set_computer_desc`, Linux only.
//
// A computer description is not a hostname: it is the free-form,
// human-readable label systemd calls `PRETTY_HOSTNAME` and keeps in
// /etc/machine-info alongside a chassis type and an icon name, and it is
// what `hostnamectl --pretty` shows. `hostname.go` already owns every
// hostname function; this is a different fact about the machine, which
// is why it lives here rather than there.
//
// **FreeBSD has no equivalent, and this refuses by name there rather
// than inventing one.** There is no file, no tool and no convention on
// FreeBSD that holds an arbitrary human-readable label for a machine the
// way /etc/machine-info does; grafting one on -- a comment in rc.conf,
// say -- would be this module deciding a convention nothing else reads,
// which is exactly the kind of invented fact this project's own
// divergence log exists to catch. So this is declared `linuxOnly`, the
// same mechanism `apparmor` uses, and a FreeBSD caller is told so by the
// platform check rather than given a value nothing wrote.
func registerSystemComputerDesc(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "system", Function: "get_computer_desc",
				Doc:       "Return this node's computer description (PRETTY_HOSTNAME in /etc/machine-info), or empty if none is set.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return systemReadComputerDesc(EtcMachineInfoPath)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "system", Function: "set_computer_desc",
				Doc: "Set this node's computer description (PRETTY_HOSTNAME in /etc/machine-info).",
				Params: []signature.Param{
					req("description", signature.String, "The human-readable description. Empty clears it."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.2",
			},
			Fn: systemSetComputerDescFn,
		},
	)
}

func systemSetComputerDescFn(c *exec.Context, args *value.Map) (any, error) {
	want := states.Str(args, "description", "")
	current, err := systemReadComputerDesc(EtcMachineInfoPath)
	if err != nil {
		return nil, fmt.Errorf("%s could not be read: %w", EtcMachineInfoPath, err)
	}
	if current == want {
		return value.MapOf("changed", false, "comment", systemComputerDescUnchangedComment(want)), nil
	}
	change := value.MapOf("computer_desc", states.Change(current, want))
	if c.Test {
		return value.MapOf(
			"changed", true,
			"comment", fmt.Sprintf(
				"the computer description would be set to %q. Nothing was changed: this was a test run.", want),
			"changes", change,
		), nil
	}
	if err := systemSetComputerDesc(c, want); err != nil {
		return nil, err
	}
	return value.MapOf(
		"changed", true,
		"comment", fmt.Sprintf("the computer description was set to %q.", want),
		"changes", change,
	), nil
}

func systemComputerDescUnchangedComment(want string) string {
	if want == "" {
		return "no computer description is set."
	}
	return fmt.Sprintf("the computer description is already %q.", want)
}

// systemReadComputerDesc reads PRETTY_HOSTNAME out of /etc/machine-info,
// following the same shell-style KEY=VALUE convention this project's
// grains package already reads /etc/os-release with. A missing file is
// an empty description, not an error: a node that has never had one set
// looks exactly like one that was explicitly cleared.
func systemReadComputerDesc(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "PRETTY_HOSTNAME" {
			continue
		}
		return systemUnquoteMachineInfo(strings.TrimSpace(val)), nil
	}
	return "", nil
}

// systemUnquoteMachineInfo reverses systemQuoteMachineInfo: it strips one
// layer of matching quotes and, for a double-quoted value, undoes the
// backslash escaping that function applies. A single-quoted value is
// returned as-is between its quotes, since systemQuoteMachineInfo never
// writes one and nothing in a single-quoted shell value is an escape.
// This is more than the simple strip-the-quotes reader grains.go uses
// for /etc/os-release, because that reader only ever consumes a value
// this project did not write; this one has to round-trip a value it
// wrote itself, quotes and all.
func systemUnquoteMachineInfo(v string) string {
	if len(v) < 2 || v[len(v)-1] != v[0] {
		return v
	}
	switch v[0] {
	case '\'':
		return v[1 : len(v)-1]
	case '"':
		inner := v[1 : len(v)-1]
		var b strings.Builder
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) && (inner[i+1] == '"' || inner[i+1] == '\\') {
				i++
			}
			b.WriteByte(inner[i])
		}
		return b.String()
	}
	return v
}

// systemQuoteMachineInfo wraps a value in double quotes for
// /etc/machine-info, escaping the two characters that would otherwise
// end the quoted string early or introduce a shell escape of their own.
func systemQuoteMachineInfo(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// systemWriteComputerDesc rewrites /etc/machine-info with a new
// PRETTY_HOSTNAME, preserving every other line and its position. A file
// that does not yet exist is created with just the one line, and an
// existing line is replaced in place rather than the file being
// rebuilt from scratch, so a node that already carries CHASSIS= or
// ICON_NAME= keeps them.
func systemWriteComputerDesc(path, desc string) error {
	assignment := "PRETTY_HOSTNAME=" + systemQuoteMachineInfo(desc)

	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var lines []string
	replaced := false
	if len(body) > 0 {
		for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			if !replaced && !strings.HasPrefix(trimmed, "#") {
				if key, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(key) == "PRETTY_HOSTNAME" {
					lines = append(lines, assignment)
					replaced = true
					continue
				}
			}
			lines = append(lines, line)
		}
	}
	if !replaced {
		lines = append(lines, assignment)
	}
	return writeAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// systemSetComputerDesc prefers `hostnamectl`, which is systemd's own
// supported way to change PRETTY_HOSTNAME and the one that notifies
// anything listening on its D-Bus signal; it falls back to writing the
// file directly, which is what a container with no systemd running
// needs. The same two-step precedent as hostname.go's own setHostname.
func systemSetComputerDesc(c *exec.Context, desc string) error {
	if c.Which("hostnamectl") != "" {
		res, err := c.Run(exec.Command{
			Argv:           []string{"hostnamectl", "set-hostname", "--pretty", desc},
			IgnoreExitCode: true,
		})
		if err == nil && res.Code == 0 {
			return nil
		}
		// hostnamectl fails inside a container with no systemd-hostnamed
		// running, where writing the file directly is both possible and
		// correct -- the same fallback hostname.go's setHostname takes.
	}
	return systemWriteComputerDesc(EtcMachineInfoPath, desc)
}
