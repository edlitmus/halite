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
	//
	// Scripted for whichever reader this platform uses, because the
	// detection goes through the `ps` module now rather than through a
	// command this file builds itself -- FreeBSD asks libxo for JSON,
	// Linux asks procps for columns.
	responses := map[string]exec.Result{}
	if runtime.GOOS == "freebsd" {
		responses[(exec.Command{Argv: []string{"ps", "--libxo=json", "-axwwo",
			strings.Join(psColumns, ",")}}).String()] = exec.Result{
			Stdout: `{"process-information":{"process":[` +
				`{"pid":"1","ppid":"0","user":"root","percent-cpu":"0.0","percent-memory":"0.1",` +
				`"rss":"1024","virtual-size":"2048","state":"Ss","command":"/sbin/init"}]}}`,
		}
	} else {
		responses[(exec.Command{Argv: []string{"ps", "--help"}}).String()] =
			exec.Result{Stdout: "usage: ps [options]\n"}
		responses[(exec.Command{Argv: []string{"ps", "-eww", "--no-headers", "-o",
			strings.Join(psColumns, ",")}}).String()] = exec.Result{
			Stdout: "    1     0 root  0.0  0.1 1024 2048 Ss   /sbin/init\n",
		}
	}
	c := &exec.Context{
		Runner: &exec.RecordingRunner{Responses: responses},
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

// A pending shutdown is found through the `ps` module's reader, so
// `reboot` works on every flavour of ps that module knows.
//
// This replaces a pair of tests for `rebootPSArgv` and
// `rebootFindShutdown`, which were this file's own second invocation of
// `ps` -- and which is exactly how `reboot.scheduled` came to answer
// "nothing is pending" on every Alpine node: the argv was procps's,
// BusyBox's ps refuses it, and the error was tolerated. There is one
// reader now, and this proves `reboot` reaches the machine through it by
// scripting a BusyBox response and expecting the shutdown to be found.
func TestAPendingShutdownIsFoundThroughThePSModule(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the BusyBox listing this scripts is a Linux one; this is %s", runtime.GOOS)
	}
	help := (exec.Command{Argv: []string{"ps", "--help"}}).String()
	listing := (exec.Command{Argv: []string{
		"ps", "-o", "pid,ppid,user,rss,vsz,stat,args"}}).String()

	c := &exec.Context{
		Runner: &exec.RecordingRunner{
			Responses: map[string]exec.Result{
				// BusyBox names itself in its usage banner, which is how
				// psArgv tells the flavours apart.
				help: {Code: 1, Stderr: "BusyBox v1.37.0 (2026-01-10) multi-call binary.\n"},
				listing: {Stdout: "PID   PPID  USER     RSS  VSZ  STAT COMMAND\n" +
					"    1     0 root      908 1636 S    /sbin/init\n" +
					" 6621     1 root     1184 1652 S    /sbin/shutdown -r +120\n"},
			},
		},
		Lookup: func(name string) string { return "/bin/" + name },
	}

	pending, err := rebootPending(c)
	if err != nil {
		t.Fatalf("rebootPending against a BusyBox listing: %v", err)
	}
	if !pending.found {
		t.Fatal("a shutdown is in the listing and rebootPending did not find it; " +
			"reboot is not reading the process table through the ps module")
	}
	if pending.pid != 6621 {
		t.Errorf("pid = %d, want 6621", pending.pid)
	}

	// And a listing with no shutdown in it is not a false positive --
	// `/sbin/init` must not match, nor anything merely mentioning the
	// word.
	c2 := &exec.Context{
		Runner: &exec.RecordingRunner{
			Responses: map[string]exec.Result{
				help: {Code: 1, Stderr: "BusyBox v1.37.0 multi-call binary.\n"},
				listing: {Stdout: "PID   PPID  USER     RSS  VSZ  STAT COMMAND\n" +
					"    1     0 root      908 1636 S    /sbin/init\n" +
					" 7000     1 root      100  200 S    grep shutdown\n"},
			},
		},
		Lookup: func(name string) string { return "/bin/" + name },
	}
	quiet, err := rebootPending(c2)
	if err != nil {
		t.Fatalf("rebootPending on a quiet machine: %v", err)
	}
	if quiet.found {
		t.Errorf("a `grep shutdown` was read as a pending reboot: %v", quiet.comment)
	}
}
