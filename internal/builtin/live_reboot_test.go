package builtin

import (
	"compress/gzip"
	"io"
	"os"
	oscmd "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `reboot`, driven against the real `shutdown` on a machine that must
// not reboot.
//
// # Why this gate is stricter than every other live test here
//
// Every other live leg in this package can be undone: a memory disk is
// detached, a jail is removed, an fstab line is deleted. This one cannot
// be undone if it goes wrong, because what goes wrong is that a machine
// reboots -- and the machine this was written on runs the fleet.
//
// So it needs `HALITE_SYSTEM_LIVE=1` **and** `HALITE_REBOOT_LIVE=1`, a
// variable no other test reads and which exists only to make this
// impossible to run by accident while running everything else.
//
// # What it actually does, and why that is safe
//
// It never reboots. It schedules one two hours out, checks that the
// module can see it, cancels it, and checks that the module can see it
// is gone. Two hours is not an arbitrary number: it is long enough that
// if every safeguard here failed at once, a person would have two hours
// to notice a `shutdown` process and kill it, and the test prints the
// countermand as its very first act so the instruction is already in the
// log before anything is scheduled.
//
// The cleanup cancels unconditionally and **fails the test** if anything
// is still pending afterwards, rather than logging it. A leftover
// scheduled reboot is the one piece of mess in this package that gets
// worse on its own.
func TestLiveRebootSchedulesAndCancelsWithoutRebooting(t *testing.T) {
	c := liveRebootSetup(t)
	r := New()

	t.Logf("if this test is interrupted, countermand what it left with: %s", liveRebootCountermand())

	// Refuse to touch a machine that already has a shutdown pending.
	// Cancelling somebody else's countdown is not this test's business,
	// and it would be indistinguishable from cancelling its own.
	if liveRebootPending(t, c, r) {
		t.Skip("a shutdown is already pending on this machine; this test will not interfere with it")
	}

	// Two hours, and the message names the test so that anyone who sees
	// the broadcast knows exactly what did it.
	const delay = int64(120)
	out, err := r.Exec.Call(c, "reboot.schedule",
		value.MapOf("delay", delay, "message", "halite live test -- being cancelled immediately"))
	// The cleanup is registered before the error is checked, because a
	// schedule that returned an error may still have scheduled.
	t.Cleanup(func() {
		if _, err := r.Exec.Call(c, "reboot.cancel", value.NewMap(0)); err != nil {
			t.Errorf("the cancel in cleanup failed -- RUN THIS AS ROOT NOW: %s: %v",
				liveRebootCountermand(), err)
		}
		if liveRebootPending(t, c, r) {
			t.Errorf("a shutdown is STILL PENDING after this test -- RUN THIS AS ROOT NOW: %s",
				liveRebootCountermand())
		}
	})
	if err != nil {
		t.Fatalf("reboot.schedule: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("scheduling a reboot reported no change: %v", out)
	}

	// The module must see what it just did, through the process table
	// rather than through its own memory of having done it.
	if !liveRebootPending(t, c, r) {
		t.Fatal("reboot.schedule reported success and reboot.scheduled cannot see a pending shutdown")
	}

	got, err := r.Exec.Call(c, "reboot.cancel", value.NewMap(0))
	if err != nil {
		t.Fatalf("reboot.cancel: %v", err)
	}
	if changed, _ := got.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("cancelling a real pending reboot reported no change: %v", got)
	}
	if liveRebootPending(t, c, r) {
		t.Fatal("reboot.cancel returned success and a shutdown is still pending")
	}
}

// Cancelling when nothing is pending converges rather than failing.
//
// A tree that wants "no reboot pending here" has to be able to say so on
// every node, including the ones that never had one. This is the safe
// half of the pair and needs no extra gate beyond root.
func TestLiveRebootCancelOnAQuietMachineIsNotAnError(t *testing.T) {
	if runtime.GOOS != "freebsd" && runtime.GOOS != "linux" {
		t.Skipf("this drives the real shutdown; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this run the real cancel")
	}
	if os.Geteuid() != 0 {
		t.Skip("cancelling a shutdown needs root")
	}
	c := &exec.Context{}
	if c.Which("shutdown") == "" {
		t.Skip("this host has no shutdown")
	}
	r := New()
	if liveRebootPending(t, c, r) {
		t.Skip("a shutdown is pending on this machine; this test will not cancel somebody else's")
	}
	got, err := r.Exec.Call(c, "reboot.cancel", value.NewMap(0))
	if err != nil {
		t.Fatalf("cancelling nothing was an error: %v", err)
	}
	// It reports no change, and says why in words rather than failing.
	if changed, _ := got.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("cancelling nothing reported a change: %v", got)
	}
	comment, _ := got.(*value.Map).GetString("comment")
	t.Logf("cancelling nothing said: %v", comment)
}

// Reading this machine costs nothing and needs no gate.
func TestLiveRebootReadsThisMachine(t *testing.T) {
	if runtime.GOOS != "freebsd" && runtime.GOOS != "linux" {
		t.Skipf("this reads a real machine; this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	r := New()

	req, err := r.Exec.Call(c, "reboot.required", value.NewMap(0))
	if err != nil {
		t.Fatalf("reboot.required: %v", err)
	}
	m := req.(*value.Map)
	known, _ := m.GetString("known")
	comment, _ := m.GetString("comment")
	t.Logf("reboot required: known=%v, %v", known, comment)
	// On FreeBSD the answer is always knowable, because freebsd-version
	// is in the base system. An unknown here means the reader broke.
	if runtime.GOOS == "freebsd" && c.Which("freebsd-version") != "" && known != true {
		t.Errorf("freebsd-version is present and the answer is still unknown: %v", comment)
	}

	boot, err := r.Exec.Call(c, "reboot.last_boot", value.NewMap(0))
	if err != nil {
		t.Fatalf("reboot.last_boot: %v", err)
	}
	up, _ := boot.(*value.Map).GetString("uptime_seconds")
	n, ok := up.(int64)
	if !ok || n <= 0 {
		t.Errorf("uptime reads %v, which is not a duration since a boot that has happened", up)
	}
	when, _ := boot.(*value.Map).GetString("time")
	t.Logf("this machine booted at %v, %v seconds ago", when, up)
}

// liveRebootSetup gates the one test that schedules anything.
func liveRebootSetup(t *testing.T) *exec.Context {
	t.Helper()
	if runtime.GOOS != "freebsd" && runtime.GOOS != "linux" {
		t.Skipf("this drives the real shutdown; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this run the real shutdown")
	}
	// The second gate, which nothing else in this package reads.
	if os.Getenv("HALITE_REBOOT_LIVE") != "1" {
		t.Skip("set HALITE_REBOOT_LIVE=1 as well to let this schedule a real reboot and " +
			"cancel it; it is deliberately not covered by HALITE_SYSTEM_LIVE alone")
	}
	if os.Geteuid() != 0 {
		t.Skip("scheduling a reboot needs root")
	}
	c := &exec.Context{}
	if c.Which("shutdown") == "" {
		t.Skip("this host has no shutdown")
	}
	return c
}

// liveRebootPending asks the module whether a shutdown is pending.
func liveRebootPending(t *testing.T, c *exec.Context, r *Registries) bool {
	t.Helper()
	got, err := r.Exec.Call(c, "reboot.scheduled", value.NewMap(0))
	if err != nil {
		t.Fatalf("reboot.scheduled: %v", err)
	}
	pending, _ := got.(*value.Map).GetString("scheduled")
	return pending == true
}

// liveRebootCountermand is the command an operator should actually run to
// stop a shutdown this test left behind.
//
// It is a function of the platform rather than a constant, because the
// constant this file used to carry was `shutdown -c` on both -- and on
// FreeBSD that is not a cancel, it is a power cycle. An instruction
// printed in a panic is followed by somebody in a hurry, so printing the
// wrong one here was worse than printing nothing: it would have told a
// person trying to save the machine to take it down. See
// rebootCancelArgv for the manual pages both halves come from.
func liveRebootCountermand() string {
	if runtime.GOOS == "freebsd" {
		return "`ps ax | grep \"[s]hutdown\"` to find the pid, then `kill -TERM <pid>` " +
			"-- NOT `shutdown -c`, which power cycles this machine"
	}
	return "`shutdown -c`"
}

// freebsdShutdownManPage is the manual page source, which ships on every
// FreeBSD install. It is read instead of running `man`, because on this
// very host `man` resolves through the Linux compatibility layer to a
// binary with no FreeBSD pages at all -- the same shadowing
// live_system_module_test.go works around for /sbin and /rescue.
const freebsdShutdownManPage = "/usr/share/man/man8/shutdown.8.gz"

// FreeBSD's own manual is held to saying what this module believes about
// `-c`, and about how a shutdown is really cancelled.
//
// # Why a test reads a manual page
//
// `rebootCancelArgv` used to hand `shutdown -c` to FreeBSD because that
// is the cancel on Linux and the flag exists on both. The flag existing
// was the whole of the evidence, and it was not evidence of anything:
// `-c` power cycles a FreeBSD machine, and it did, to the host this
// module was being written on. A test that checks a flag is *listed*
// would have passed that evening -- live_system_module_test.go has one,
// and it did pass. So this checks the sentence beside the flag instead.
//
// If a future FreeBSD ever makes `-c` a cancel, this fails, and whoever
// sees it should revisit rebootCancelArgv rather than this test.
func TestFreeBSDsManualSaysWhatThisModuleBelievesAboutCancelling(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skipf("this reads FreeBSD's own manual page; this is %s", runtime.GOOS)
	}
	f, err := os.Open(freebsdShutdownManPage)
	if err != nil {
		t.Skipf("%s is not on this host: %v", freebsdShutdownManPage, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("%s is not readable gzip: %v", freebsdShutdownManPage, err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("%s could not be read: %v", freebsdShutdownManPage, err)
	}
	body := string(raw)

	// The paragraph for -c, in mdoc: `.It Fl c` up to the next `.It`.
	const flagC = ".It Fl c"
	i := strings.Index(body, flagC)
	if i < 0 {
		t.Fatalf("%s does not document -c at all", freebsdShutdownManPage)
	}
	para := body[i+len(flagC):]
	if j := strings.Index(para, ".It "); j >= 0 {
		para = para[:j]
	}
	if !strings.Contains(para, "power cycled") {
		t.Errorf("this module refuses to pass -c on FreeBSD because the manual calls it a power "+
			"cycle, and the manual no longer does. Revisit rebootCancelArgv:\n%s", para)
	}
	if strings.Contains(strings.ToLower(para), "cancel") {
		t.Errorf("-c is documented as cancelling something, which contradicts this module's "+
			"platform split. Revisit rebootCancelArgv:\n%s", para)
	}

	// And the mechanism the module does use, named in the same page.
	//
	// The phrases are fragments rather than the whole sentence because
	// this is mdoc source, not rendered output: the page writes "killing
	// the .Nm process (a .Dv SIGTERM should suffice)", so the macro names
	// interrupt the sentence a reader sees. These two fragments are the
	// parts no macro splits.
	for _, phrase := range []string{"can be canceled by killing the", "SIGTERM"} {
		if !strings.Contains(body, phrase) {
			t.Errorf("the manual no longer says %q, which is the mechanism rebootCancelArgv "+
				"builds a `kill -TERM` from", phrase)
		}
	}
}

// The FreeBSD cancel mechanism, driven end to end against a real process
// on the real process table -- without a real `shutdown` anywhere near it.
//
// # What this covers that nothing else could
//
// The cancel this module performs on FreeBSD is: read the process table,
// find the pending shutdown, send it SIGTERM. Every part of that is real
// machinery -- `ps` on this host, `rebootFindShutdown`'s parse of what it
// printed, and a real signal delivered to a real pid -- and none of it
// was demonstrated, because the only way to make a genuine `shutdown`
// process appear is to schedule a genuine reboot. That is the gated test
// above, and the first attempt at it power cycled this machine.
//
// So this makes a process the finder will match on its own terms: a copy
// of `sleep` named `shutdown`, started by the test, owned by the test's
// own account, needing no privilege to signal. What is exercised is the
// path, not the tool: if `rebootFindShutdown` stopped matching what this
// host's `ps` prints, or the argv stopped being a working `kill`, this
// fails. What it deliberately does not prove is that a real
// `shutdown(8)` dies on SIGTERM -- FreeBSD's manual is the authority for
// that, and the test above checks it still says so.
func TestTheFreeBSDCancelMechanismWorksAgainstARealProcess(t *testing.T) {
	if runtime.GOOS != "freebsd" && runtime.GOOS != "linux" {
		t.Skipf("this drives a real process table; this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	if c.Which("ps") == "" || c.Which("kill") == "" {
		t.Skip("this host has no ps or kill")
	}
	sleep := c.Which("sleep")
	if sleep == "" {
		t.Skip("this host has no sleep to stand in for a shutdown")
	}

	// A copy named `shutdown`, because rebootFindShutdown matches the
	// command's own basename -- which is the point of it, and is why a
	// stray `grep shutdown` does not count as a pending reboot.
	dir := t.TempDir()
	stand := filepath.Join(dir, "shutdown")
	body, err := os.ReadFile(sleep)
	if err != nil {
		t.Skipf("%s could not be copied: %v", sleep, err)
	}
	if err := os.WriteFile(stand, body, 0o755); err != nil {
		t.Fatalf("the stand-in could not be written: %v", err)
	}

	cmd := oscmd.Command(stand, "600")
	if err := cmd.Start(); err != nil {
		t.Skipf("the stand-in could not be started: %v", err)
	}
	// Reaped no matter which assertion below fails first.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// It has to be visible to the module's own reader, through the real
	// `ps`, before anything is asserted about cancelling it.
	var pending rebootPendingShutdown
	for i := 0; i < 50; i++ {
		if pending, err = rebootPending(c); err != nil {
			t.Fatalf("rebootPending: %v", err)
		}
		if pending.found && pending.pid == int64(cmd.Process.Pid) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !pending.found {
		t.Fatalf("a process named shutdown (pid %d) is running and rebootPending did not see it",
			cmd.Process.Pid)
	}
	if pending.pid != int64(cmd.Process.Pid) {
		t.Skipf("rebootPending found pid %d, not this test's %d -- something else named shutdown "+
			"is on this machine and it is not this test's business to signal it",
			pending.pid, cmd.Process.Pid)
	}

	// The command the module would build, run as the module would run it.
	argv, err := rebootCancelArgv("freebsd", pending.pid)
	if err != nil {
		t.Fatalf("rebootCancelArgv: %v", err)
	}
	if res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true}); err != nil {
		t.Fatalf("%v could not be run: %v", argv, err)
	} else if res.Code != 0 {
		t.Fatalf("%v exited %d: %s", argv, res.Code, res.Stderr)
	}

	// SIGTERM, not SIGKILL: the process must have chosen to die.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stand-in is still running five seconds after the module's own cancel command")
	}

	// And the module now reports what it did, through the process table
	// rather than through its memory of having done it.
	after, err := rebootPending(c)
	if err != nil {
		t.Fatalf("rebootPending after the cancel: %v", err)
	}
	if after.found && after.pid == int64(cmd.Process.Pid) {
		t.Errorf("the cancel returned success and pid %d is still pending", cmd.Process.Pid)
	}
}
