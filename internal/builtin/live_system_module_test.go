package builtin

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// `system`, against the real tools on the real FreeBSD host this was
// written on -- and nothing else.
//
// # What is, and is not, run here
//
// Both tests below invoke a real binary with an argument vector that
// cannot make it act: `shutdown` with no arguments at all, which its own
// usage line says needs a mandatory `time`, and `/rescue/date --help`,
// an option BSD `date` does not know, which the earlier capture in this
// file's package comment already showed fails with "illegal option" and
// its usage line -- neither read reaches the code path that would set
// anything. This is exactly the exception the task that produced this
// file names: "read their usage text with a bare invocation that cannot
// act... nothing more." `halt`, `poweroff`, `reboot` and a real `date`
// invocation that supplies a value are never run here, or anywhere else
// in this package's tests, because none of them has a safe bare form:
// each would act on the machine running the test.
//
// # Why this matters more than a fixture would
//
// systemPowerArgv and systemClockArgv were written from FreeBSD's real
// usage lines, not from memory of what `shutdown(8)` or `date(1)` looks
// like -- this project's own history has four defects that came from a
// fixture written in a module's own spelling. These two tests are that
// derivation held to the tool itself: if a future FreeBSD drops a flag
// this module depends on, or reorders `date`'s positional fields, one of
// these fails before anything downstream does.

// liveShutdownPath is the real, setuid-root FreeBSD binary. It is not
// looked up by name through the shell's PATH: this host's Linux
// compatibility layer shadows /bin and /usr/bin with a GNU userland (see
// the dev-host-is-freebsd-with-a-linux-shell note this project's own
// history keeps), so resolving "shutdown" through a Context with no
// Lookup override would silently find nothing useful to test against.
// /sbin is not shadowed, and this is where the earlier capture in
// system_module.go's package comment came from.
const liveShutdownPath = "/sbin/shutdown"

// liveDatePath is FreeBSD's own statically linked rescue copy, present
// on every install for exactly the case a live host's real binaries are
// needed and its /bin cannot be trusted to be its own.
const liveDatePath = "/rescue/date"

func TestRealShutdownUsageNamesTheFlagsThisModuleAssumes(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("this checks the real FreeBSD /sbin/shutdown")
	}
	if _, err := os.Stat(liveShutdownPath); err != nil {
		t.Skipf("%s is not on this host: %v", liveShutdownPath, err)
	}

	c := &exec.Context{}
	res, err := c.Run(exec.Command{Argv: []string{liveShutdownPath}, IgnoreExitCode: true})
	if err != nil {
		t.Fatalf("%s could not be run: %v", liveShutdownPath, err)
	}
	usage := res.Stdout + res.Stderr
	if usage == "" {
		t.Fatal("shutdown with no arguments printed nothing; it should have refused with its usage line")
	}
	// Every flag systemPowerArgv and rebootScheduleArgv build a command
	// with: -r (reboot), -p (poweroff-flavoured shutdown), -c (cancel,
	// used by reboot.go, checked here too since it is the same binary).
	for _, flag := range []string{"-r", "-p", "-h", "-c"} {
		if !strings.Contains(usage, flag) {
			t.Errorf("shutdown's usage does not mention %q, which this module's argv table assumes it takes:\n%s",
				flag, usage)
		}
	}
}

func TestRealDateUsageNamesTheFormatThisModuleAssumes(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("this checks the real FreeBSD date, via /rescue")
	}
	if _, err := os.Stat(liveDatePath); err != nil {
		t.Skipf("%s is not on this host: %v", liveDatePath, err)
	}

	c := &exec.Context{}
	// --help is not one of BSD date's options, so it is refused with a
	// usage line and nothing else -- this was checked interactively
	// before being written into a test, per this project's own rule
	// against a fixture nobody ran.
	res, err := c.Run(exec.Command{Argv: []string{liveDatePath, "--help"}, IgnoreExitCode: true})
	if err != nil {
		t.Fatalf("%s could not be run: %v", liveDatePath, err)
	}
	usage := res.Stdout + res.Stderr
	const settingForm = "[[[[[[cc]yy]mm]dd]HH]MM[.SS] | new_date]"
	if !strings.Contains(usage, settingForm) {
		t.Errorf("date's usage does not contain %q, which systemClockArgv's FreeBSD row assumes is the "+
			"setting format:\n%s", settingForm, usage)
	}
}
