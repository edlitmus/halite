package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The scheduling command is a table keyed on platform, checked from any
// host.
//
// This is `quota`'s pattern and it is here for a sharper version of
// `quota`'s reason: a reboot command written for one platform's tool and
// never run on the other is a defect whose first symptom is a machine
// that did or did not come back.
func TestEveryPlatformsRebootCommandIsTheOneItsToolTakes(t *testing.T) {
	for _, tc := range []struct {
		goos    string
		delay   int64
		message string
		want    []string
	}{
		{"freebsd", 5, "", []string{"shutdown", "-r", "+5"}},
		{"linux", 5, "", []string{"shutdown", "-r", "+5"}},
		{"freebsd", 1, "halite: kernel update", []string{"shutdown", "-r", "+1", "halite: kernel update"}},
		{"linux", 90, "halite: kernel update", []string{"shutdown", "-r", "+90", "halite: kernel update"}},
		// A message that is only spaces is no message, not an empty
		// trailing argument, which shutdown would take as the text and
		// broadcast as a blank line.
		{"linux", 5, "   ", []string{"shutdown", "-r", "+5"}},
	} {
		got, err := rebootScheduleArgv(tc.goos, tc.delay, tc.message)
		if err != nil {
			t.Errorf("%s/%d: %v", tc.goos, tc.delay, err)
			continue
		}
		if !equalStrings(got, tc.want) {
			t.Errorf("%s/%d: %v, want %v", tc.goos, tc.delay, got, tc.want)
		}
	}
	for _, goos := range []string{"darwin", "windows", "openbsd"} {
		if _, err := rebootScheduleArgv(goos, 5, ""); err == nil {
			t.Errorf("%s was given a reboot command this build has never run there", goos)
		}
	}
}

// There is no zero delay, and the refusal says where to go instead.
//
// A scheduled reboot with no delay is an immediate reboot wearing the
// name of the cancellable one. The whole reason this function is
// separate from `system.reboot` is the gap in between, so accepting a
// zero would quietly erase the distinction the module is built around.
func TestAScheduledRebootCannotBeImmediate(t *testing.T) {
	for _, delay := range []int64{0, -1, -60} {
		_, err := rebootScheduleArgv("freebsd", delay, "")
		if err == nil {
			t.Errorf("a delay of %d was accepted", delay)
			continue
		}
		if !strings.Contains(err.Error(), "system.reboot") {
			t.Errorf("the refusal for %d does not say what to use instead: %v", delay, err)
		}
	}
}

// The cancel is *not* the same command on the two families.
//
// This test replaces one called TestTheCancelCommandIsTheSameOnBothFamilies,
// which asserted the defect: it checked that both platforms were handed
// `shutdown -c`, which cancels on Linux and power cycles on FreeBSD.
// Believing the two families shared this flag was the whole mistake, and
// a test that wrote the belief down could only confirm it -- so this one
// names the difference, and the case below that no FreeBSD argv may
// contain -c is the assertion the old name made impossible.
func TestTheCancelCommandDiffersByFamily(t *testing.T) {
	for _, c := range []struct {
		name string
		goos string
		pid  int64
		want []string
		fail bool
	}{
		{"linux cancels with the flag and needs no pid", "linux", 0, []string{"shutdown", "-c"}, false},
		{"freebsd signals the process it was given", "freebsd", 28318, []string{"kill", "-TERM", "28318"}, false},
		{"freebsd refuses without a pid", "freebsd", 0, nil, true},
		{"freebsd refuses a nonsense pid", "freebsd", -1, nil, true},
		{"an unrun platform gets nothing", "windows", 1, nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := rebootCancelArgv(c.goos, c.pid)
			if c.fail {
				if err == nil {
					t.Fatalf("%s/%d was given %v", c.goos, c.pid, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s/%d: %v", c.goos, c.pid, err)
			}
			if !equalStrings(got, c.want) {
				t.Errorf("%s/%d built %v, want %v", c.goos, c.pid, got, c.want)
			}
		})
	}
}

// FreeBSD must never be handed -c by this function, for any pid.
//
// The flag means "power cycle the machine" there, and it is obeyed on any
// host with a BMC the ipmi(4) driver supports. This is the one assertion
// that would have caught the defect before it reached a real machine, so
// it is written as its own test rather than as a row above.
func TestFreeBSDIsNeverHandedThePowerCycleFlag(t *testing.T) {
	for _, pid := range []int64{-1, 0, 1, 28318} {
		argv, err := rebootCancelArgv("freebsd", pid)
		if err != nil {
			continue
		}
		for _, arg := range argv {
			if arg == "-c" {
				t.Fatalf("pid %d built %v, and freebsd's -c power cycles the machine", pid, argv)
			}
		}
	}
}

// systemd's scheduled-shutdown file is read for a description, and its
// presence alone already means "pending".
//
// The fixture is the shape logind writes: USEC, WARN_WALL and MODE, one
// KEY=value per line. The last two cases are the ones that matter --
// neither a missing MODE nor an unparseable USEC may turn a file that
// exists into an answer of "nothing is scheduled", because systemd
// deletes the file when a shutdown is cancelled.
func TestTheSystemdScheduleIsDescribedWithoutBeingBelievedTooPrecisely(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
	}{
		{
			"a full file names the verb and the time",
			"USEC=1788234000000000\nWARN_WALL=1\nMODE=reboot\n",
			"reboot at 2026-09-01T03:40:00Z",
		},
		{
			"a file with no mode still reports the time",
			"USEC=1788234000000000\nWARN_WALL=1\n",
			"at 2026-09-01T03:40:00Z",
		},
		{
			"a file with an unreadable time still names the verb",
			"USEC=not-a-number\nMODE=poweroff\n",
			"poweroff, at a time " + rebootSystemdScheduledFile + " does not spell readably",
		},
		{
			"an empty file is still a pending shutdown",
			"",
			rebootSystemdScheduledFile + " exists but names neither a mode nor a time",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := rebootDescribeSystemdSchedule(c.body); got != c.want {
				t.Errorf("described as %q, want %q", got, c.want)
			}
		})
	}
}

// FreeBSD's kern.boottime is read for its number, not its date.
//
// The fixture is what this host really printed. The trailing date is
// localised and formatted for a person; the number beside it is neither,
// which is why it is the one taken.
func TestTheBootTimeIsReadFromTheNumberNotTheDate(t *testing.T) {
	const real = "{ sec = 1787754162, usec = 472586 } Wed Aug 26 07:22:42 2026\n"
	sec, ok := rebootParseBoottime(real)
	if !ok {
		t.Fatal("a real kern.boottime did not parse")
	}
	if sec != 1787754162 {
		t.Errorf("read %d", sec)
	}
	for _, bad := range []string{"", "nonsense", "{ usec = 1 }", "{ sec = notanumber }"} {
		if _, ok := rebootParseBoottime(bad); ok {
			t.Errorf("%q was read as a boot time", bad)
		}
	}
}

// A pending shutdown is found by the command's own name, not by a
// substring of the line.
//
// The listing contains this project's own processes while its tests run.
// Matching anywhere in the line would find `go test ... reboot.go`, an
// editor, or a log tail, and report a reboot nobody scheduled -- which a
// state would then treat as "already pending" and decline to act on.
func TestAPendingShutdownIsFoundByTheCommandNotByTheLine(t *testing.T) {
	const listing = `  1 /sbin/init
 42 /usr/bin/vi internal/builtin/reboot.go
 77 tail -f /var/log/shutdown.log
 91 go test ./internal/builtin/ -run Shutdown
`
	if _, _, found := rebootFindShutdown(listing); found {
		t.Error("a shutdown was reported for a listing that has none, only lines mentioning one")
	}

	const pending = listing + " 99 /sbin/shutdown -r +5 halite: kernel update\n"
	pid, cmd, found := rebootFindShutdown(pending)
	if !found {
		t.Fatal("a real pending shutdown was not found")
	}
	if pid != 99 {
		t.Errorf("pid %d, want 99", pid)
	}
	if !strings.Contains(cmd, "-r +5") {
		t.Errorf("the command is not carried back: %q", cmd)
	}
}

// The Debian marker's companion file names the packages that asked.
//
// "A reboot is required" and "a reboot is required because these six
// packages changed" are different amounts of help to whoever has to
// decide whether now is the time.
func TestTheRebootRequiredPackagesAreDeduplicated(t *testing.T) {
	// Written the way the real file is: one package per line, and apt
	// appends without checking, so duplicates are ordinary.
	got := rebootDedupeLines("linux-image-generic\nlibssl3\nlinux-image-generic\n\n  libssl3  \n")
	if len(got) != 2 {
		t.Fatalf("read %v, want two distinct packages", got)
	}
	if got[0] != "linux-image-generic" || got[1] != "libssl3" {
		t.Errorf("read %v, want them in the order the file lists them", got)
	}
}

// The two ps columns are asked for with a separate -o each.
//
// `-o pid=,command=` is the Linux idiom and it is what this module used
// to send. FreeBSD's ps(1) reads the `=` as introducing a replacement
// header that runs to the end of the argument, so it saw one keyword
// headed with the literal string ",command=", printed a single column of
// bare pids, and exited 0. rebootFindShutdown then matched nothing on
// every FreeBSD node, forever, with no error anywhere -- see
// rebootPSArgv for the capture.
//
// This is a unit test rather than a live one because the failure is
// invisible in the output: the command succeeds and the lines look
// plausible. The shape of the request is the only thing worth pinning.
func TestThePSColumnsAreAskedForSeparately(t *testing.T) {
	argv := rebootPSArgv()

	for _, arg := range argv {
		if strings.Contains(arg, ",") {
			t.Errorf("%v joins keywords with a comma; FreeBSD reads everything after the "+
				"first `=` as a header, so only the first column survives", argv)
		}
	}

	// Both columns are actually requested, each behind its own -o.
	var columns []string
	for i, arg := range argv {
		if arg == "-o" && i+1 < len(argv) {
			columns = append(columns, argv[i+1])
		}
	}
	if !equalStrings(columns, []string{"pid=", "command="}) {
		t.Errorf("%v asks for columns %v, want a separate -o for pid= and command=", argv, columns)
	}
}

// Cancelling on a machine with nothing pending reports no change, and
// runs no cancel command at all.
//
// # The exit status could not answer this, and was trusted anyway
//
// The Linux branch used to skip the "is anything pending" question and
// read `shutdown -c`'s exit status instead. systemd exits **0 whether or
// not there was anything to cancel**, so on a quiet machine this
// returned `changed: true` with the comment "the pending shutdown was
// cancelled" -- a state that would report converging work forever, and a
// module claiming to have done something no one asked and nothing did.
// It was found on a real AlmaLinux node, by the live test of the same
// name, because no unit test covered the quiet case. This is that test.
func TestCancellingWithNothingPendingReportsNoChange(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		t.Skipf("reboot.cancel is built for linux and freebsd; this is %s", runtime.GOOS)
	}

	// A process table with no shutdown in it. `ps` is the only command
	// that may run: reaching the cancel itself is the defect.
	psKey := "ps -ax -o pid= -o command="
	c := &exec.Context{
		Runner: &exec.RecordingRunner{
			Responses: map[string]exec.Result{
				psKey: {Stdout: "  1 /sbin/init\n 42 /usr/bin/sshd\n"},
			},
		},
		Lookup: func(name string) string { return "/usr/bin/" + name },
	}

	out, err := rebootCancel(c)
	if err != nil {
		t.Fatalf("cancelling nothing was an error: %v", err)
	}
	m, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("rebootCancel returned %T", out)
	}
	if changed, _ := m.GetString("changed"); changed != false {
		comment, _ := m.GetString("comment")
		t.Errorf("changed = %v with nothing pending, comment %q; want false", changed, comment)
	}

	// And it never reached the cancel. On FreeBSD that command would be
	// a `kill`; on Linux a `shutdown -c` whose exit status says nothing.
	rec, _ := c.Runner.(*exec.RecordingRunner)
	for _, ran := range rec.Ran {
		if s := ran.String(); strings.Contains(s, "shutdown") || strings.Contains(s, "kill") {
			t.Errorf("a cancel was run against a machine with nothing pending: %q", s)
		}
	}
}
