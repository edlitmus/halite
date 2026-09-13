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

// rebootPlatforms are the platforms whose reboot machinery this build
// has been run against.
var rebootPlatforms = []string{"linux", "freebsd"}

// registerReboot installs the `reboot` module of SPEC 15.2 and the
// `reboot` state of SPEC 15.5.
//
// # This module does not reboot anything
//
// That is the first thing to know about it, and it is deliberate.
// `system.reboot` and `system.halt` are the verbs that act now; this
// module is the layer above them -- whether a reboot is *needed*,
// whether one is already *pending*, scheduling one far enough ahead that
// somebody can stop it, and stopping it.
//
// The split is not tidiness. A reboot is the one operation on a node
// that cannot be undone by the thing that performed it: the agent that
// issues it is gone before it can report, and if the node does not come
// back there is nothing left running to fix it. So the function an
// automated tree reaches for should be the one with a delay in front of
// it and a cancel beside it, and the immediate verb should be something
// an operator types on purpose.
//
// # "A reboot is required" is a different fact on every platform
//
// There is no portable answer, and the wrong ones are tempting:
//
//   - **FreeBSD** has an exact one. `freebsd-version -k` reports the
//     kernel that is installed and `-r` the kernel that is running, so
//     when they differ a kernel update has landed and not yet taken
//     effect. Nothing else on the system says this.
//   - **Debian and Ubuntu** have a convention: `/run/reboot-required`,
//     written by package scripts. It is a fact about what the package
//     manager did, not about the kernel, which is why the file's
//     companion lists which packages asked.
//   - **Everywhere else** the honest answer is "this build does not
//     know", and that is what it returns -- not `false`, which a tree
//     would read as "no reboot needed" and act on.
//
// **`uname -r` is deliberately not used for any of this.** On the host
// this was written on it answers `5.15.0` -- the Linux compatibility
// layer's number -- while the machine is running FreeBSD 15.1. A check
// built on it would compare a kernel version against a lie.
func registerReboot(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "reboot", Function: "required",
				Doc: "Report whether this node needs a reboot, and what says so.",
				// It reads. The answer includes "this build cannot tell
				// on this platform", which is not the same as no.
				TestMode:  signature.TestNotApplicable,
				Platforms: rebootPlatforms,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return rebootRequired(c) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "reboot", Function: "scheduled",
				Doc:       "Report whether a shutdown or reboot is already pending on this node.",
				TestMode:  signature.TestNotApplicable,
				Platforms: rebootPlatforms,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return rebootScheduled(c) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "reboot", Function: "last_boot",
				Doc:       "Return the time this node last booted.",
				TestMode:  signature.TestNotApplicable,
				Platforms: rebootPlatforms,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return rebootLastBoot(c) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "reboot", Function: "schedule",
				Doc: "Schedule a reboot, far enough ahead that it can be cancelled.",
				Params: []signature.Param{
					opt("delay", signature.Int, int64(rebootDefaultDelayMinutes),
						"Minutes from now. The minimum is 1; there is deliberately no zero."),
					opt("message", signature.String, "", "A message to send to anyone logged in."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  rebootPlatforms,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return rebootSchedule(c, states.Int(args, "delay", rebootDefaultDelayMinutes),
					states.Str(args, "message", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "reboot", Function: "cancel",
				Doc:        "Cancel a pending shutdown or reboot.",
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  rebootPlatforms,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return rebootCancel(c) },
		},
	)

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "reboot", Function: "scheduled",
			Doc: "Ensure a reboot is scheduled when this node needs one.",
			Params: []signature.Param{
				opt("delay", signature.Int, int64(rebootDefaultDelayMinutes),
					"Minutes from now, when one has to be scheduled."),
				opt("message", signature.String, "", "A message to send to anyone logged in."),
				opt("only_if_required", signature.Bool, true,
					"Schedule only when `reboot.required` says one is needed. Turning this off "+
						"schedules a reboot unconditionally on every node this state reaches."),
			},
			Mutates:    true,
			TestMode:   signature.TestReliable,
			Privileges: []string{"root"},
			Platforms:  rebootPlatforms,
			Section:    "15.5",
		},
		Fn: rebootScheduledState,
	})
}

// rebootDefaultDelayMinutes is how far ahead a scheduled reboot goes
// when a tree does not say.
//
// One minute would be honest and useless: the point of scheduling rather
// than rebooting is that somebody can countermand it, and a minute is
// not long enough to notice. Two is not either. Five is long enough to
// read an alert and type `reboot.cancel`, and short enough that a tree
// which meant it does not wait around.
const rebootDefaultDelayMinutes = 5

// rebootRequired answers the question each platform answers differently.
func rebootRequired(c *exec.Context) (any, error) {
	out := value.NewMap(4)
	switch runtime.GOOS {
	case "freebsd":
		return rebootRequiredFreeBSD(c, out)
	case "linux":
		return rebootRequiredLinux(c, out)
	}
	// Not reached through the registry, which refuses the platform
	// first, but a direct caller deserves the same answer.
	return nil, fmt.Errorf("this build does not know how to tell whether %s needs a reboot",
		runtime.GOOS)
}

// rebootRequiredFreeBSD compares the installed kernel with the running
// one.
//
// `freebsd-version` is in the base system and is the only thing that
// reports both numbers. Its `-k` is what is on disk and its `-r` is what
// is in memory, so a difference is a kernel update that has landed and
// not yet taken effect -- which is precisely the question, and is not
// answerable from `uname` at all.
func rebootRequiredFreeBSD(c *exec.Context, out *value.Map) (any, error) {
	if c.Which("freebsd-version") == "" {
		out.Set("required", nil)
		out.Set("known", false)
		out.Set("comment", "this node has no `freebsd-version`, which is the only thing in the "+
			"base system that reports the installed and running kernels separately")
		return out, nil
	}
	installed, err := rebootFreeBSDVersion(c, "-k")
	if err != nil {
		return nil, err
	}
	running, err := rebootFreeBSDVersion(c, "-r")
	if err != nil {
		return nil, err
	}
	required := installed != running
	out.Set("required", required)
	out.Set("known", true)
	out.Set("source", "freebsd-version")
	if required {
		out.Set("comment", fmt.Sprintf(
			"the installed kernel is %s and the running one is %s, so a kernel update has "+
				"landed and has not taken effect", installed, running))
	} else {
		out.Set("comment", fmt.Sprintf("the installed and running kernels are both %s", running))
	}
	return out, nil
}

// rebootFreeBSDVersion reads one of freebsd-version's numbers.
func rebootFreeBSDVersion(c *exec.Context, flag string) (string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"freebsd-version", flag}, IgnoreExitCode: true})
	if err != nil {
		return "", fmt.Errorf("freebsd-version could not be run on this node: %w", err)
	}
	if res.Code != 0 {
		return "", fmt.Errorf("`freebsd-version %s` exited %d: %s",
			flag, res.Code, strings.TrimSpace(firstLine(res.Stderr+res.Stdout)))
	}
	v := strings.TrimSpace(res.Stdout)
	if v == "" {
		return "", fmt.Errorf("`freebsd-version %s` printed nothing", flag)
	}
	return v, nil
}

// rebootRequiredLinux reads the Debian convention, and says so when the
// node does not follow it.
//
// The file is written by package scripts rather than by the kernel, so
// it is a statement about what the package manager did. Its companion,
// `reboot-required.pkgs`, lists which packages asked, and that is worth
// passing on: "a reboot is required" and "a reboot is required because
// libssl changed" are different amounts of help.
//
// A node without the file is **not** reported as "no reboot needed"
// unless it is a distribution that would have written one. On anything
// else the honest answer is that this build cannot tell, because a tree
// reading `false` would act on it.
func rebootRequiredLinux(c *exec.Context, out *value.Map) (any, error) {
	const marker = "/run/reboot-required"
	if _, err := os.Stat(marker); err == nil {
		out.Set("required", true)
		out.Set("known", true)
		out.Set("source", marker)
		if pkgs := rebootRequiredPackages(); len(pkgs) > 0 {
			out.Set("packages", pkgs)
			out.Set("comment", fmt.Sprintf("%s exists; %d package(s) asked for it", marker, len(pkgs)))
		} else {
			out.Set("comment", marker+" exists")
		}
		return out, nil
	}
	// The convention is Debian's and Ubuntu's. Absence of the file on a
	// distribution that writes them is a real "no"; absence on one that
	// never writes them says nothing at all.
	if rebootLinuxWritesTheMarker() {
		out.Set("required", false)
		out.Set("known", true)
		out.Set("source", marker)
		out.Set("comment", "this distribution writes "+marker+" when a reboot is needed, and it is not there")
		return out, nil
	}
	out.Set("required", nil)
	out.Set("known", false)
	out.Set("comment", "this build can only answer this on Debian and Ubuntu, which write "+
		marker+", and on FreeBSD, where `freebsd-version` reports the installed and running "+
		"kernels separately. Reporting `false` here would be a guess a tree would act on")
	return out, nil
}

// rebootRequiredPackages lists the packages that asked for the reboot.
func rebootRequiredPackages() []any {
	body, err := os.ReadFile("/run/reboot-required.pkgs")
	if err != nil {
		return nil
	}
	return rebootDedupeLines(string(body))
}

// rebootDedupeLines reads a one-per-line list, in order, without
// repeats.
//
// apt appends to reboot-required.pkgs without checking what is already
// there, so the same package appears once per upgrade that asked.
func rebootDedupeLines(body string) []any {
	seen := map[string]bool{}
	var out []any
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(line); line != "" && !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	return out
}

// rebootLinuxWritesTheMarker reports whether this distribution is one
// whose package scripts write /run/reboot-required.
//
// Read from os-release rather than assumed: `ID_LIKE=debian` catches the
// derivatives without naming each one, and a distribution that is not in
// that family gets "cannot tell" instead of a wrong "no".
func rebootLinuxWritesTheMarker() bool {
	body, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"'`)
		if key != "ID" && key != "ID_LIKE" {
			continue
		}
		for _, id := range strings.Fields(val) {
			if id == "debian" || id == "ubuntu" {
				return true
			}
		}
	}
	return false
}

// rebootScheduleArgv builds the command that schedules a reboot.
//
// A table keyed on platform, so every row is checkable from any host --
// the pattern `quota` uses, and for the reason `quota` needed it: a
// command written for one platform's tool looks perfectly correct until
// it runs on the other.
//
// Both families spell the delay `+N`, and both take a trailing message,
// which is the part worth getting right: it is what a logged-in operator
// sees, and it is the difference between an unexplained countdown and a
// sentence naming the tree that started it.
func rebootScheduleArgv(goos string, delayMinutes int64, message string) ([]string, error) {
	if delayMinutes < 1 {
		return nil, fmt.Errorf(
			"a scheduled reboot needs at least one minute of delay; %d was asked for. "+
				"Rebooting now is `system.reboot`, which is a different function on purpose: "+
				"this one exists so that there is time to countermand it", delayMinutes)
	}
	switch goos {
	case "linux", "freebsd":
		argv := []string{"shutdown", "-r", fmt.Sprintf("+%d", delayMinutes)}
		if message = strings.TrimSpace(message); message != "" {
			argv = append(argv, message)
		}
		return argv, nil
	}
	return nil, fmt.Errorf("this build does not know how to schedule a reboot on %s", goos)
}

// rebootCancelArgv builds the command that countermands a pending
// shutdown on one platform.
//
// # The two platforms share no flag here, and the wrong one is fatal
//
// `shutdown -c` is the cancel on Linux, where systemd documents `-c` as
// cancelling a pending shutdown. It is the flag the idiom reaches for and
// it is correct there.
//
// On FreeBSD `-c` is not a cancel at all. Its shutdown(8) says:
//
//	-c  The system is power cycled (power turned off and then back on)
//	    at the specified time. If the hardware doesn't support power
//	    cycle, the system will be rebooted.
//
// On a machine whose BMC the ipmi(4) driver supports -- which is what
// this project's own fleet runs on -- it is obeyed. This module carried
// `-c` on both platforms for one evening, and that evening the
// development host logged the only power cycle in its entire syslog
// history and went down mid-session:
//
//	shutdown[28318]: power-cycle by ed:
//
// Which invocation passed the flag is not recoverable from the logs, and
// it was not this function's own argv: `shutdown(8)` requires a
// mandatory `time` argument, so a bare `shutdown -c` reaches
// `usage()` -- /usr/src/sbin/shutdown/shutdown.c:178 -- rather than
// acting. FreeBSD was being handed the wrong flag *and* the wrong arity
// for it, and no test could reach either, because both live on the far
// side of a command no test may run.
//
// The real mechanism is in the same manual page, two paragraphs above
// the flag list: "A scheduled shutdown can be canceled by killing the
// shutdown process (a SIGTERM should suffice)." That is a pid rather
// than a flag, which is why this function takes one, and why the FreeBSD
// branch refuses to build any command without it instead of falling back
// on something that looks close.
func rebootCancelArgv(goos string, pid int64) ([]string, error) {
	switch goos {
	case "linux":
		return []string{"shutdown", "-c"}, nil
	case "freebsd":
		if pid <= 0 {
			return nil, errors.New(
				"cancelling on freebsd means sending SIGTERM to the pending shutdown process " +
					"and no pid was given; there is deliberately no flag fallback here, because " +
					"freebsd's `shutdown -c` power cycles the machine")
		}
		return []string{"kill", "-TERM", strconv.FormatInt(pid, 10)}, nil
	}
	return nil, fmt.Errorf("this build does not know how to cancel a shutdown on %s", goos)
}

// rebootSchedule schedules a reboot that can be stopped.
func rebootSchedule(c *exec.Context, delayMinutes int64, message string) (any, error) {
	argv, err := rebootScheduleArgv(runtime.GOOS, delayMinutes, message)
	if err != nil {
		return nil, err
	}
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`", argv[0])
	}
	if why := rebootBlocked(); why != "" {
		return nil, errors.New(why)
	}
	out := value.NewMap(4)
	out.Set("command", exec.Command{Argv: argv}.String())
	if c.Test {
		out.Set("changed", true)
		out.Set("comment", fmt.Sprintf(
			"a reboot would be scheduled for %d minute(s) from now. Nothing was changed: "+
				"this was a test run.", delayMinutes))
		return out, nil
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("shutdown could not be run on this node: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(),
			res.Code, strings.TrimSpace(firstLine(res.Stderr+res.Stdout)))
	}
	out.Set("changed", true)
	out.Set("delay_minutes", delayMinutes)
	out.Set("comment", fmt.Sprintf(
		"a reboot is scheduled for %d minute(s) from now; `reboot.cancel` stops it",
		delayMinutes))
	return out, nil
}

// rebootBlocked reports the system's own interlock, or "".
//
// FreeBSD's shutdown refuses outright while /var/run/noshutdown exists,
// and it is the operator's way of saying "not this machine, not now".
// Reading it here turns the refusal into a sentence naming the file
// rather than a non-zero exit a tree has to interpret.
func rebootBlocked() string {
	const interlock = "/var/run/noshutdown"
	if runtime.GOOS != "freebsd" {
		return ""
	}
	if _, err := os.Stat(interlock); err != nil {
		return ""
	}
	return interlock + " exists, and shutdown refuses to run while it does. Somebody put it " +
		"there to stop exactly this; remove it deliberately rather than working around it"
}

// rebootCancel countermands a pending shutdown.
//
// Cancelling when nothing is pending is **not** an error. A state that
// wants "no reboot pending on this node" should be able to say so and
// converge, and the tool's own complaint that there was nothing to
// cancel is the answer to a different question.
//
// FreeBSD cannot be asked to cancel without a pid -- see
// rebootCancelArgv for what its one cancel-shaped flag really does -- so
// the process table is read first there, and a quiet machine is answered
// without running anything at all. Linux is not asked the same question
// first: a shutdown scheduled under systemd is held by systemd rather
// than by a resident `shutdown` process, so an empty process table would
// prove nothing about whether one is pending. There `shutdown -c` is
// asked directly and its exit status is the answer.
func rebootCancel(c *exec.Context) (any, error) {
	var pid int64
	if runtime.GOOS == "freebsd" {
		pending, err := rebootPending(c)
		if err != nil {
			return nil, err
		}
		if !pending.found {
			out := value.NewMap(2)
			out.Set("changed", false)
			out.Set("comment", "no shutdown is pending on this node, so there was nothing to cancel")
			return out, nil
		}
		pid = pending.pid
	}
	argv, err := rebootCancelArgv(runtime.GOOS, pid)
	if err != nil {
		return nil, err
	}
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`", argv[0])
	}
	out := value.NewMap(3)
	out.Set("command", exec.Command{Argv: argv}.String())
	if c.Test {
		out.Set("changed", true)
		out.Set("comment", "a pending shutdown would be cancelled. Nothing was changed: this was a test run.")
		return out, nil
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("shutdown could not be run on this node: %w", err)
	}
	said := strings.TrimSpace(res.Stderr + res.Stdout)
	out.Set("changed", res.Code == 0)
	if res.Code == 0 {
		out.Set("comment", "the pending shutdown was cancelled")
	} else {
		out.Set("comment", "there was no shutdown to cancel: "+firstLine(said))
	}
	return out, nil
}

// rebootScheduled reports whether a shutdown is already pending.
func rebootScheduled(c *exec.Context) (any, error) {
	pending, err := rebootPending(c)
	if err != nil {
		return nil, err
	}
	out := value.NewMap(3)
	out.Set("scheduled", pending.found)
	// Only a shutdown some process is holding has a pid to report; one
	// systemd is holding has none, which is the point of the struct.
	if pending.pid > 0 {
		out.Set("pid", pending.pid)
	}
	out.Set("comment", pending.comment)
	return out, nil
}

// rebootPendingShutdown is what this node says about a pending shutdown:
// whether there is one, which process holds it if a process does, and a
// sentence naming the evidence.
type rebootPendingShutdown struct {
	found   bool
	pid     int64
	comment string
}

// rebootPending answers whether a shutdown is pending, and how it knows.
//
// # Two mechanisms, because there are two
//
// A shutdown scheduled by FreeBSD's `shutdown(8)`, or by a Linux without
// systemd, is a *process* sitting on a timer, so the process table is
// where it shows -- the same place `ps.list` looks. That is also why a
// pending reboot survives nothing: one scheduled and then countermanded
// by a restart is simply gone.
//
// systemd does not work that way. `shutdown -r +5` under systemd hands
// the schedule to logind and the command exits, so `ps` is empty while a
// reboot is very much pending; what holds it is
// /run/systemd/shutdown/scheduled. Reading only the process table there
// would have this module answer "no shutdown is pending" on the very
// machine it had just scheduled one on, and `reboot.scheduled`'s whole
// job is to be believed on that question.
func rebootPending(c *exec.Context) (rebootPendingShutdown, error) {
	res, err := c.Run(exec.Command{Argv: rebootPSArgv(), IgnoreExitCode: true})
	if err != nil {
		return rebootPendingShutdown{}, fmt.Errorf("ps could not be run on this node: %w", err)
	}
	if pid, cmd, ok := rebootFindShutdown(res.Stdout); ok {
		return rebootPendingShutdown{found: true, pid: pid,
			comment: "a shutdown is pending: " + cmd}, nil
	}
	if runtime.GOOS == "linux" {
		if body, err := os.ReadFile(rebootSystemdScheduledFile); err == nil {
			return rebootPendingShutdown{found: true,
				comment: "systemd is holding a pending shutdown: " +
					rebootDescribeSystemdSchedule(string(body))}, nil
		}
	}
	return rebootPendingShutdown{comment: "no shutdown is pending on this node"}, nil
}

// rebootPSArgv asks for two columns with no headers, in the one spelling
// both platforms read the same way.
//
// **`-o pid=,command=` is not that spelling, and it fails silently.**
// It is the Linux idiom and it is wrong on FreeBSD, whose ps(1) reads an
// `=` as introducing *a replacement header that runs to the end of the
// argument*. So FreeBSD takes the whole of `pid=,command=` as one
// keyword -- `pid`, headed with the literal string `,command=` -- and
// prints a single column of bare numbers:
//
//	$ ps -axo pid=,command=
//	,command=
//	        0
//	        1
//
// Nothing errors. `ps` exits 0, the output is real, and every line has a
// pid in the field this module parses -- it simply never has a command
// in the second field, so rebootFindShutdown matches nothing, ever.
// `reboot.scheduled` answered "no shutdown is pending" on every FreeBSD
// node whatever was pending, and `reboot.cancel`, which now finds its
// pid that way, would have had nothing to cancel. A separate `-o` per
// column is unambiguous on both.
func rebootPSArgv() []string {
	return []string{"ps", "-ax", "-o", "pid=", "-o", "command="}
}

// rebootSystemdScheduledFile is where logind records a shutdown that has
// been scheduled and has not yet begun. systemd removes it on cancel, so
// its mere presence is the answer.
const rebootSystemdScheduledFile = "/run/systemd/shutdown/scheduled"

// rebootDescribeSystemdSchedule turns that file into a sentence.
//
// It is a short list of KEY=value lines, of which two matter: USEC is
// when, in microseconds since the epoch, and MODE is which verb. Neither
// is required to be there for the answer to be "yes, one is pending" --
// the file's existence already settled that -- so an unreadable USEC
// costs a detail in a comment and never turns into a "no".
func rebootDescribeSystemdSchedule(body string) string {
	var mode, when string
	for _, line := range strings.Split(body, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "MODE":
			mode = val
		case "USEC":
			if usec, err := strconv.ParseInt(val, 10, 64); err == nil {
				when = time.Unix(usec/1e6, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	switch {
	case mode != "" && when != "":
		return mode + " at " + when
	case mode != "":
		return mode + ", at a time " + rebootSystemdScheduledFile + " does not spell readably"
	case when != "":
		return "at " + when
	}
	return rebootSystemdScheduledFile + " exists but names neither a mode nor a time"
}

// rebootFindShutdown looks for a shutdown process in a ps listing.
//
// Matched on the command's own first word rather than anywhere in the
// line, because `grep shutdown` would match this build's own tests, an
// editor with shutdown.go open, and a log tail.
func rebootFindShutdown(out string) (pid int64, command string, found bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		base := fields[1]
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if base == "shutdown" {
			return n, strings.Join(fields[1:], " "), true
		}
	}
	return 0, "", false
}

// rebootLastBoot reports when the node came up.
func rebootLastBoot(c *exec.Context) (any, error) {
	sec, source, err := rebootBootTime(c)
	if err != nil {
		return nil, err
	}
	t := time.Unix(sec, 0).UTC()
	out := value.NewMap(4)
	out.Set("epoch", sec)
	out.Set("time", t.Format(time.RFC3339))
	out.Set("source", source)
	out.Set("uptime_seconds", int64(time.Since(t).Seconds()))
	return out, nil
}

// rebootBootTime reads the boot time from whichever source the platform
// has.
//
// FreeBSD records the moment itself in `kern.boottime`, which is exact.
// Linux has `btime` in /proc/stat, which is the same thing; deriving it
// by subtracting uptime from the current time would agree most of the
// time and drift whenever the clock was stepped, which is exactly when
// somebody is looking.
func rebootBootTime(c *exec.Context) (int64, string, error) {
	switch runtime.GOOS {
	case "freebsd":
		res, err := c.Run(exec.Command{
			Argv:           []string{"sysctl", "-n", "kern.boottime"},
			IgnoreExitCode: true,
		})
		if err != nil || res.Code != 0 {
			return 0, "", fmt.Errorf("`sysctl -n kern.boottime` could not be read: %v", err)
		}
		sec, ok := rebootParseBoottime(res.Stdout)
		if !ok {
			return 0, "", fmt.Errorf("kern.boottime does not carry a seconds field: %q",
				strings.TrimSpace(res.Stdout))
		}
		return sec, "kern.boottime", nil
	case "linux":
		body, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, "", fmt.Errorf("/proc/stat could not be read: %w", err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if rest, ok := strings.CutPrefix(line, "btime "); ok {
				sec, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
				if err != nil {
					return 0, "", fmt.Errorf("/proc/stat's btime is not a number: %q", rest)
				}
				return sec, "/proc/stat btime", nil
			}
		}
		return 0, "", errors.New("/proc/stat has no btime line")
	}
	return 0, "", fmt.Errorf("this build does not know where %s records its boot time", runtime.GOOS)
}

// rebootParseBoottime reads the seconds out of FreeBSD's kern.boottime.
//
// It prints a struct and a rendered date together:
//
//	{ sec = 1787754162, usec = 472586 } Wed Aug 26 07:22:42 2026
//
// The number is taken rather than the date, because the date is
// localised and formatted for a person and the number is neither.
func rebootParseBoottime(out string) (int64, bool) {
	// **The key is matched on a boundary, and that is not fussiness.**
	// `usec` ends in `sec`, and the struct carries both, so a plain
	// substring search for `sec = ` finds whichever comes first in the
	// string rather than the one that was asked for. On a real
	// kern.boottime `sec` happens to come first and the bug is
	// invisible; against `{ usec = 1 }` it reads the microseconds as
	// the epoch and reports a boot in 1970. This project's own test
	// caught it before the tool ever could.
	i := rebootIndexKey(out, "sec")
	if i < 0 {
		return 0, false
	}
	rest := out[i:]
	end := strings.IndexAny(rest, ",} \t")
	if end < 0 {
		return 0, false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64)
	if err != nil {
		return 0, false
	}
	return sec, true
}

// rebootIndexKey finds `<key> = ` where key is a whole word, and returns
// the offset just past the equals and its space.
func rebootIndexKey(out, key string) int {
	needle := key + " = "
	for from := 0; ; {
		i := strings.Index(out[from:], needle)
		if i < 0 {
			return -1
		}
		at := from + i
		if at == 0 || !isKeyByte(out[at-1]) {
			return at + len(needle)
		}
		from = at + len(needle)
	}
}

// isKeyByte reports whether a byte could be part of a key's name, which
// is what makes `usec` and `sec` distinguishable.
func isKeyByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// rebootScheduledState ensures a reboot is pending when one is needed.
//
// **It schedules rather than reboots, and there is no option to make it
// reboot now.** A state runs unattended, across a fleet, on a timer. The
// difference between "every node schedules a reboot five minutes out"
// and "every node reboots" is the five minutes in which somebody can
// notice and stop it, and a state is exactly the context where nobody is
// watching. An operator who wants it now has `system.reboot`.
//
// A node that already has one pending is converged, not scheduled twice.
func rebootScheduledState(c *exec.Context, args *value.Map) (states.Result, error) {
	delay := states.Int(args, "delay", rebootDefaultDelayMinutes)
	message := states.Str(args, "message", "")
	onlyIfRequired := states.Bool(args, "only_if_required", true)

	if onlyIfRequired {
		required, err := rebootRequired(c)
		if err != nil {
			return states.False(fmt.Sprintf("whether a reboot is needed could not be read: %v", err)), nil
		}
		m := required.(*value.Map)
		known, _ := m.GetString("known")
		if known != true {
			comment, _ := m.GetString("comment")
			return states.False(fmt.Sprintf(
				"this state was asked to act only if a reboot is required, and that cannot be "+
					"determined here: %v. Set only_if_required to false to schedule one anyway.",
				comment)), nil
		}
		if need, _ := m.GetString("required"); need != true {
			comment, _ := m.GetString("comment")
			return states.True(fmt.Sprintf("no reboot is required: %v", comment)), nil
		}
	}

	pending, err := rebootScheduled(c)
	if err != nil {
		return states.False(fmt.Sprintf("whether a shutdown is pending could not be read: %v", err)), nil
	}
	if already, _ := pending.(*value.Map).GetString("scheduled"); already == true {
		comment, _ := pending.(*value.Map).GetString("comment")
		return states.True(fmt.Sprintf("a reboot is already pending, so this changed nothing: %v",
			comment)), nil
	}

	changes := value.MapOf("reboot", states.Change("none pending", fmt.Sprintf("in %d minute(s)", delay)))
	if c.Test {
		return states.WouldChange(fmt.Sprintf(
			"a reboot would be scheduled for %d minute(s) from now.", delay), changes), nil
	}
	if _, err := rebootSchedule(c, delay, message); err != nil {
		return states.False(fmt.Sprintf("a reboot could not be scheduled: %v", err)), nil
	}
	return states.Changed(fmt.Sprintf(
		"a reboot is scheduled for %d minute(s) from now; `reboot.cancel` stops it", delay),
		changes), nil
}
