package builtin

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The `service` module's **launchd** provider, driven against the real
// `launchctl` on a real Mac.
//
// # What was assumed until now
//
// Everything. `evidence.go` has said, in these words, that "the launchd,
// sysvinit and openrc providers ... have not been run at all", and
// plan.md §7 item 19b names launchd as the cheapest of the three now
// that `fleet.yml`'s `macos` leg is a Mac running as root. `mac_service`
// is a module of its own and is *not* this code path: it is an alias
// layer, and nothing has ever asked `service.start` on a Mac which
// provider answered.
//
// # What this brings with it
//
// A LaunchDaemon of its own — `org.halite.live-probe`, a `/bin/sh` loop
// that sleeps, with `RunAtLoad` and `KeepAlive` both false so that
// loading it does not start it and stopping it does not restart it.
// That combination is what makes launchd's two separate facts
// observable: a job can be *known* to launchd and not running, which is
// the distinction `Status` claims to make and which no test has ever
// watched it make.
//
// The probe is loaded with `launchctl bootstrap system` and removed with
// `launchctl bootout`, deliberately **not** with the `load -w`/`unload -w`
// pair: those write the same persistent disable store the module's
// `Enable`/`Disable` drive, so a setup built on them would be setting
// the very fact the test is about to measure.
//
// # What is checked against what
//
// Never the module's own read-back. Running is settled with `launchctl
// print system/<label>`, which is a different command from the
// `launchctl list <label>` the module reads, and the persistent
// enable/disable answer is read from `launchctl print-disabled system`
// with a **deliberately more tolerant** rule than the module's — both
// spellings launchd has used, rather than the one the module matches on.
// Two readers that agree by construction agree about nothing.
//
// # Why it skips everywhere else
//
// `HALITE_SYSTEM_LIVE=1` and root, the gate the rest of the macOS row
// shares. Loading a system-wide LaunchDaemon and writing launchd's
// disable store is not something to do because somebody typed
// `go test ./...`. Run it:
//
//	sudo HALITE_SYSTEM_LIVE=1 go test -run TestLiveMacService -v ./internal/builtin/

const liveLaunchdLabel = "org.halite.live-probe"

const liveLaunchdPlistPath = "/Library/LaunchDaemons/" + liveLaunchdLabel + ".plist"

// The probe daemon. `sleep` in a loop rather than a single long sleep so
// that a SIGTERM that is delivered to the shell rather than to the child
// still ends the job promptly.
const liveLaunchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + liveLaunchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/sh</string>
		<string>-c</string>
		<string>while :; do /bin/sleep 1; done</string>
	</array>
	<key>RunAtLoad</key>
	<false/>
	<key>KeepAlive</key>
	<false/>
</dict>
</plist>
`

// launchdLive gates on a Mac that has been offered up, and on the module
// actually choosing the provider this file is about. A macOS machine on
// which `pickServiceProvider` answered something other than launchd
// would make every assertion below a test of some other code.
func launchdLive(t *testing.T) *exec.Context {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a Mac you can throw away; this loads a system LaunchDaemon")
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("launchd is macOS's, and this is %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_SYSTEM_LIVE is set on a Mac and this is not root; bootstrapping a system daemon needs it")
	}
	c := realCtx(t)
	if c.Which("launchctl") == "" {
		t.Fatal("macOS with no launchctl on PATH")
	}
	p, err := pickServiceProvider(c)
	if err != nil {
		t.Fatalf("no init system was recognised on this Mac: %v", err)
	}
	if p.Name() != "mac_service" {
		t.Fatalf("the service module picked the %s provider on macOS; this file tests launchd", p.Name())
	}
	return c
}

// installProbeDaemon writes the plist, bootstraps it into the system
// domain, and takes every trace of it back out afterwards — including
// the disable-store entry, which outlives the plist and is keyed by
// label alone.
func installProbeDaemon(t *testing.T, c *exec.Context) {
	t.Helper()
	if err := os.WriteFile(liveLaunchdPlistPath, []byte(liveLaunchdPlist), 0o644); err != nil {
		t.Fatalf("writing the probe daemon: %v", err)
	}
	t.Cleanup(func() {
		for _, argv := range [][]string{
			{"launchctl", "bootout", "system/" + liveLaunchdLabel},
			{"launchctl", "enable", "system/" + liveLaunchdLabel},
		} {
			_, _ = c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		}
		_ = os.Remove(liveLaunchdPlistPath)
		res, _ := c.Run(exec.Command{
			Argv:           []string{"launchctl", "print", "system/" + liveLaunchdLabel},
			IgnoreExitCode: true,
		})
		if res.Code == 0 {
			t.Errorf("the probe daemon is still loaded after cleanup:\n%s", res.Stdout)
		}
	})
	// bootstrap refuses a plist it considers insecurely owned, so a
	// failure here is about the file rather than about launchd.
	res, err := c.Run(exec.Command{
		Argv:           []string{"launchctl", "bootstrap", "system", liveLaunchdPlistPath},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("launchctl bootstrap: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("launchctl bootstrap system %s exited %d: %s",
			liveLaunchdPlistPath, res.Code, res.Stderr+res.Stdout)
	}
}

// launchdPrint is the truth this file measures against: a different
// command from the `launchctl list <label>` the module reads.
func launchdPrint(t *testing.T, c *exec.Context) (out string, loaded bool) {
	t.Helper()
	res, err := c.Run(exec.Command{
		Argv:           []string{"launchctl", "print", "system/" + liveLaunchdLabel},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("launchctl print: %v", err)
	}
	return res.Stdout + res.Stderr, res.Code == 0
}

// launchdRunningPID reads the job's pid out of `launchctl print`. A job
// that is loaded and idle has no pid line at all, which is the state
// this probe sits in between the tests below.
func launchdRunningPID(t *testing.T, c *exec.Context) (int, string) {
	t.Helper()
	out, loaded := launchdPrint(t, c)
	if !loaded {
		t.Fatalf("the probe daemon is not loaded:\n%s", out)
	}
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "pid = ") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(ln, "pid = ")))
		if err != nil {
			t.Fatalf("`launchctl print` printed %q, which is not a pid line this test can read", ln)
		}
		return pid, out
	}
	return 0, out
}

// launchdSettleLimit is deliberately far longer than anything this test
// expects to wait, because it is measuring how long launchd actually
// takes rather than asserting a number somebody guessed. launchd
// throttles a job's respawn — a job asked to start again within ten
// seconds of its last start is held until that window passes — so a
// deadline anywhere near ten seconds is a test that passes on timing.
const launchdSettleLimit = 60 * time.Second

// waitForLaunchdPID polls the tool — not the module — until the job is
// running or is not, and returns the pid it settled on together with how
// long the machine took to get there. launchd's `start` and `stop` are
// asynchronous: they ask, and the answer arrives when the process has
// actually been spawned or reaped. The caller logs the wait, because
// "the module returned" and "the job is running" being different
// moments is the thing this file exists to establish.
func waitForLaunchdPID(t *testing.T, c *exec.Context, what string, wantRunning bool) (int, time.Duration) {
	t.Helper()
	started := time.Now()
	deadline := started.Add(launchdSettleLimit)
	logged := false
	for {
		pid, out := launchdRunningPID(t, c)
		if (pid != 0) == wantRunning {
			return pid, time.Since(started)
		}
		if !logged {
			// What launchd says about the job while it is not yet where
			// the caller asked it to be. A wait nobody can explain is
			// worth less than a wait with the machine's own account of
			// it beside it.
			logged = true
			t.Logf("%s: while waiting, `launchctl print` says: %s", what, launchdStateLines(out))
		}
		if time.Now().After(deadline) {
			state := "running"
			if !wantRunning {
				state = "stopped"
			}
			t.Fatalf("after %s the probe daemon never became %s within %s; `launchctl print` says:\n%s",
				what, state, launchdSettleLimit, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// launchdStateLines keeps the handful of lines of `launchctl print` that
// say what a job is doing, out of the hundred or so that say how it is
// configured.
func launchdStateLines(out string) string {
	var kept []string
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		for _, want := range []string{"state = ", "pid = ", "runs = ", "last exit", "throttle"} {
			if strings.HasPrefix(ln, want) || strings.Contains(ln, want) {
				kept = append(kept, ln)
				break
			}
		}
	}
	if len(kept) == 0 {
		return "nothing about its state"
	}
	return strings.Join(kept, "; ")
}

// launchdDisableStore reads the persistent override for the probe label
// out of `launchctl print-disabled system`, interpreting **both**
// spellings launchd has printed — `=> disabled` and the older
// `=> true` — rather than the one spelling the module matches on. The
// point is to disagree with the module if the module is reading the
// wrong one.
//
// It returns the raw line as well, because a third spelling this test
// does not know is the finding rather than a failure to be explained.
func launchdDisableStore(t *testing.T, c *exec.Context) (disabled bool, present bool, raw string) {
	t.Helper()
	res, err := c.Run(exec.Command{
		Argv:           []string{"launchctl", "print-disabled", "system"},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("launchctl print-disabled: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("launchctl print-disabled system exited %d: %s", res.Code, res.Stderr+res.Stdout)
	}
	quoted := `"` + liveLaunchdLabel + `"`
	for _, ln := range strings.Split(res.Stdout, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, quoted) {
			continue
		}
		answer := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(
			strings.TrimPrefix(ln, quoted)), ";"))
		answer = strings.TrimSpace(strings.TrimPrefix(answer, "=>"))
		switch answer {
		case "disabled", "true":
			return true, true, ln
		case "enabled", "false":
			return false, true, ln
		default:
			t.Fatalf("`launchctl print-disabled system` says %q for the probe, "+
				"which is neither spelling this test knows; the module reads this file", ln)
		}
	}
	return false, false, ""
}

// **A job launchd knows about and is not running.** The provider's
// Status comment claims `launchctl list <label>` exits 0 for a loaded
// job and carries a `"PID" =` key only while it is really running, and
// that the exit code alone would answer the wrong question. Nothing had
// watched it; this is the state the probe is in the moment it loads.
func TestLiveMacServiceKnowsALoadedJobIsNotRunning(t *testing.T) {
	c := launchdLive(t)
	installProbeDaemon(t, c)
	r := New()

	pid, out := launchdRunningPID(t, c)
	if pid != 0 {
		t.Fatalf("the probe daemon started itself at load; RunAtLoad is false:\n%s", out)
	}
	// Printed in full, and on purpose. `launchdSpawnCount` reads this
	// output and the fixture behind its unit test has to be captured
	// from a real launchd rather than written from the manual page --
	// which is what this log line is for. It is the job at rest, before
	// anything below has started it.
	t.Logf("`launchctl print system/%s` on a loaded, never-run job:\n%s", liveLaunchdLabel, out)
	// The exit code says "known to launchd" and the module must not read
	// it as "running".
	res, err := c.Run(exec.Command{
		Argv:           []string{"launchctl", "list", liveLaunchdLabel},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 0 {
		t.Fatalf("`launchctl list %s` exited %d on a job that is bootstrapped: %s",
			liveLaunchdLabel, res.Code, res.Stderr+res.Stdout)
	}

	running, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveLaunchdLabel))
	if err != nil {
		t.Fatalf("service.status: %v", err)
	}
	if running != false {
		t.Errorf("service.status = %v for a loaded, idle job; `launchctl list` printed:\n%s", running, res.Stdout)
	}

	// And a label launchd has never heard of is neither running nor an
	// error, which is the branch a `service.dead` state takes on a node
	// where the job was never installed.
	absent, err := r.Exec.Call(c, "service.status", value.MapOf("name", "org.halite.no-such-job"))
	if err != nil {
		t.Fatalf("service.status on an unknown label: %v", err)
	}
	if absent != false {
		t.Errorf("service.status = %v for a label launchd does not know", absent)
	}
}

// **start / stop / restart, watched through a different command.**
//
// The restart assertion is the pid changing rather than the job merely
// being up afterwards: a Restart that did nothing at all would leave a
// running job running, and pass a test that only asked whether it was.
//
// And each call is asserted to have **finished what it asked for by the
// time it returns**, which is the assertion the systemd provider's test
// makes about `ActiveState` and the reason that provider awaits its job.
// This is where launchd differs and where the first run of this file
// found a defect: `launchctl start` returns as soon as the request is
// queued, and launchd throttles a respawn to ten seconds, so a restart
// reported a service restarted while it was down for 10.03 seconds
// (DIVERGENCE 5.122). The elapsed times are logged rather than bounded
// by a number somebody chose, because the number wanted here is a
// measurement.
func TestLiveMacServiceStartsStopsAndRestarts(t *testing.T) {
	c := launchdLive(t)
	installProbeDaemon(t, c)
	r := New()

	callStarted := time.Now()
	if _, err := r.Exec.Call(c, "service.start", value.MapOf("name", liveLaunchdLabel)); err != nil {
		t.Fatalf("service.start: %v", err)
	}
	t.Logf("service.start returned after %s", time.Since(callStarted))
	// The job is running **now**, not eventually. Read from the tool
	// rather than from the module, so that a provider which returned too
	// early cannot agree with itself about it.
	first, out := launchdRunningPID(t, c)
	if first == 0 {
		t.Errorf("service.start returned and the job is not running; `launchctl print` says: %s",
			launchdStateLines(out))
		first, _ = waitForLaunchdPID(t, c, "service.start", true)
	}
	if running, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveLaunchdLabel)); err != nil || running != true {
		t.Errorf("service.status = %v, %v while pid %d is running", running, err, first)
	}

	callStarted = time.Now()
	if _, err := r.Exec.Call(c, "service.restart", value.MapOf("name", liveLaunchdLabel)); err != nil {
		t.Fatalf("service.restart on a running job: %v", err)
	}
	restartCall := time.Since(callStarted)
	// This is the measurement that matters: how long the call took, and
	// whether the job was really back when it returned. Before the
	// provider awaited the respawn the call took five milliseconds and
	// the job was down for ten seconds afterwards.
	second, out := launchdRunningPID(t, c)
	t.Logf("service.restart returned after %s with the job at pid %d", restartCall, second)
	if second == 0 {
		t.Errorf("service.restart returned and the job is not running; `launchctl print` says: %s",
			launchdStateLines(out))
		second, _ = waitForLaunchdPID(t, c, "service.restart", true)
	}
	if second == first {
		t.Errorf("service.restart left pid %d in place; the job was never restarted", first)
	}

	if _, err := r.Exec.Call(c, "service.stop", value.MapOf("name", liveLaunchdLabel)); err != nil {
		t.Fatalf("service.stop: %v", err)
	}
	// `launchctl stop` is asynchronous in the other direction and
	// nothing here waits on it, so the reaping is polled and timed
	// rather than asserted at the instant of return.
	_, stopWait := waitForLaunchdPID(t, c, "service.stop", false)
	t.Logf("service.stop: the job was reaped %s after the call returned", stopWait)
	if stopped, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveLaunchdLabel)); err != nil || stopped != false {
		t.Errorf("service.status = %v, %v after the job was reaped", stopped, err)
	}
}

// **Restarting a service that is not running has to start it.**
//
// Separate from the test above because it is the case an SLS file
// reaches by accident and the one this provider is most likely to get
// wrong: `Restart` is `Stop` then `Start`, and launchd's `stop` fails
// on a job that is already stopped. A `service.running` state with a
// `watch` requirement restarts whatever it finds, running or not.
func TestLiveMacServiceRestartsAJobThatIsNotRunning(t *testing.T) {
	c := launchdLive(t)
	installProbeDaemon(t, c)
	r := New()

	if pid, out := launchdRunningPID(t, c); pid != 0 {
		t.Fatalf("the probe daemon is running before the test started:\n%s", out)
	}
	if _, err := r.Exec.Call(c, "service.restart", value.MapOf("name", liveLaunchdLabel)); err != nil {
		t.Fatalf("service.restart on a stopped job: %v", err)
	}
	if pid, out := launchdRunningPID(t, c); pid == 0 {
		t.Errorf("service.restart returned on a stopped job and it is not running; `launchctl print` says: %s",
			launchdStateLines(out))
		waitForLaunchdPID(t, c, "service.restart on a stopped job", true)
	}
}

// **enable / disable, against launchd's own disable store.**
//
// The store is persistent and keyed by label: it survives the plist
// being removed, and it is what decides whether the job comes back after
// a reboot. The module reads it with `launchctl print-disabled system`
// and this test reads the same output by a looser rule, so that a
// spelling the module does not match on shows up as a disagreement
// rather than as two readers being wrong together.
func TestLiveMacServiceEnablesAndDisablesInTheStore(t *testing.T) {
	c := launchdLive(t)
	installProbeDaemon(t, c)
	r := New()

	if _, err := r.Exec.Call(c, "service.disable", value.MapOf("name", liveLaunchdLabel)); err != nil {
		t.Fatalf("service.disable: %v", err)
	}
	disabled, present, raw := launchdDisableStore(t, c)
	// Logged because the ledger claims a spelling, and a claim about
	// another program's output is worth having that program's own words
	// behind it in the run this release was cut from.
	t.Logf("`launchctl print-disabled system` after service.disable: %q (present: %v)", raw, present)
	if !present {
		t.Fatal("service.disable wrote nothing `launchctl print-disabled system` reports for the label")
	}
	if !disabled {
		t.Errorf("after service.disable the store says %q", raw)
	}
	en, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveLaunchdLabel))
	if err != nil {
		t.Fatalf("service.enabled: %v", err)
	}
	if en != false {
		t.Errorf("service.enabled = %v while launchd's store says %q", en, raw)
	}
	if dis, err := r.Exec.Call(c, "service.disabled", value.MapOf("name", liveLaunchdLabel)); err != nil || dis != true {
		t.Errorf("service.disabled = %v, %v while launchd's store says %q", dis, err, raw)
	}

	if _, err := r.Exec.Call(c, "service.enable", value.MapOf("name", liveLaunchdLabel)); err != nil {
		t.Fatalf("service.enable: %v", err)
	}
	disabled, present, raw = launchdDisableStore(t, c)
	t.Logf("`launchctl print-disabled system` after service.enable: %q (present: %v)", raw, present)
	if present && disabled {
		t.Errorf("after service.enable the store still says %q", raw)
	}
	en, err = r.Exec.Call(c, "service.enabled", value.MapOf("name", liveLaunchdLabel))
	if err != nil {
		t.Fatalf("service.enabled: %v", err)
	}
	if en != true {
		// A label `launchctl enable` removes from the store entirely
		// leaves the provider falling back to Status, which answers a
		// different question — that is the case this names by hand.
		t.Errorf("service.enabled = %v after service.enable; the store line is %q (present: %v)", en, raw, present)
	}
}

// **get_all, available and missing**, which on launchd all read
// `launchctl list`.
func TestLiveMacServiceListsTheProbe(t *testing.T) {
	c := launchdLive(t)
	installProbeDaemon(t, c)
	r := New()

	out, err := r.Exec.Call(c, "service.get_all", value.NewMap(0))
	if err != nil {
		t.Fatalf("service.get_all: %v", err)
	}
	names, _ := out.([]any)
	if len(names) < 20 {
		t.Fatalf("a Mac has more than %d launchd jobs; the listing was not parsed", len(names))
	}
	found := false
	for _, n := range names {
		if s, _ := n.(string); s == liveLaunchdLabel {
			found = true
		}
	}
	if !found {
		t.Errorf("the probe daemon is not in service.get_all")
	}

	if avail, err := r.Exec.Call(c, "service.available", value.MapOf("name", liveLaunchdLabel)); err != nil || avail != true {
		t.Errorf("service.available = %v, %v for a bootstrapped job", avail, err)
	}
	if missing, err := r.Exec.Call(c, "service.missing", value.MapOf("name", "org.halite.no-such-job")); err != nil || missing != true {
		t.Errorf("service.missing = %v, %v for a label launchd does not know", missing, err)
	}
}

// **Masking is systemd's alone**, and the refusal has to say so rather
// than report a Mac's services as unmasked. `serviceMasker`'s own
// comment is that a provider which cannot answer says which init system
// it is; that promise has never been read on a machine without systemd.
func TestLiveMacServiceRefusesToMask(t *testing.T) {
	c := launchdLive(t)
	r := New()

	_, err := r.Exec.Call(c, "service.masked", value.MapOf("name", liveLaunchdLabel))
	if err == nil {
		t.Fatal("service.masked answered on launchd, which has no such concept")
	}
	if !strings.Contains(err.Error(), "mac_service") {
		t.Errorf("the refusal does not name the init system: %v", err)
	}
}

// **A restart outside launchd's respawn throttle does not wait for it.**
//
// launchd holds a respawn until ten seconds after the job's last spawn
// (its `ThrottleInterval`), and the provider now waits for the respawn
// rather than reporting a restart that has not happened (DIVERGENCE
// 5.122). That wait is the throttle's and nobody else's -- `launchctl
// kickstart -k` was measured taking the same ten seconds inside the window
// (DIVERGENCE 5.149) -- so the claim worth holding is the other half: a
// job that has been up longer than the window, which is what a service
// being reloaded after a configuration change is, comes back at once.
//
// And a second restart straight after the first is inside the window
// again, so it waits. That is logged, not asserted, because the number is
// launchd's: it is the cost of a tree that restarts one service twice in
// a run.
func TestLiveMacServiceRestartOutsideTheThrottleIsImmediate(t *testing.T) {
	c := launchdLive(t)
	installProbeDaemon(t, c)
	r := New()

	if _, err := r.Exec.Call(c, "service.start", value.MapOf("name", liveLaunchdLabel)); err != nil {
		t.Fatal(err)
	}
	// Past the window, measured from the spawn the start made.
	time.Sleep(11 * time.Second)

	for i, want := range []string{"outside the window", "inside the window"} {
		pid, _ := launchdRunningPID(t, c)
		started := time.Now()
		if _, err := r.Exec.Call(c, "service.reload", value.MapOf("name", liveLaunchdLabel)); err != nil {
			t.Fatalf("service.reload %s: %v", want, err)
		}
		took := time.Since(started)
		now, out := launchdRunningPID(t, c)
		t.Logf("service.reload %s returned after %s, pid %d -> %d", want, took.Round(time.Millisecond), pid, now)
		if now == 0 || now == pid {
			t.Errorf("service.reload %s returned and the job was not respawned: %s", want, launchdStateLines(out))
		}
		// Five seconds is half the throttle: a reload that waited for it
		// cannot come in under this, and one that did not has room to
		// spare on a loaded runner.
		if i == 0 && took > 5*time.Second {
			t.Errorf("a reload of a job up for longer than the throttle took %s; it should not wait for it", took)
		}
	}
}

// **A per-user job: a LaunchAgent in the console user's gui domain.**
//
// Every command the provider ran named, or defaulted to, the system
// domain. Against an agent loaded in gui/501 and running, `service.status`
// said false, `service.start` and `service.stop` failed with launchctl
// exiting 3, and `service.enabled` read the system store (DIVERGENCE
// 5.150) -- so `service.dead` would have called a running agent stopped.
//
// Each step is checked with `launchctl print gui/<uid>/<label>`, not
// through the module.
func TestLiveMacServiceManagesAConsoleUsersAgent(t *testing.T) {
	c := launchdLive(t)
	r := New()
	const label = "org.halite.live-agent"
	path := "/Library/LaunchAgents/" + label + ".plist"

	console, err := c.Run(exec.Command{Argv: []string{"stat", "-f", "%u", "/dev/console"}})
	if err != nil {
		t.Fatal(err)
	}
	uid := strings.TrimSpace(console.Stdout)
	if uid == "" || uid == "0" {
		t.Skipf("nobody is logged in at the console (%q), so there is no gui domain", uid)
	}
	domain := "gui/" + uid
	target := domain + "/" + label

	if err := os.WriteFile(path, []byte(strings.ReplaceAll(liveLaunchdPlist, liveLaunchdLabel, label)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, argv := range [][]string{{"launchctl", "bootout", target}, {"launchctl", "enable", target}} {
			_, _ = c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		}
		_ = os.Remove(path)
		if res, _ := c.Run(exec.Command{Argv: []string{"launchctl", "print", target}, IgnoreExitCode: true}); res.Code == 0 {
			t.Errorf("the agent is still loaded after cleanup")
		}
	})
	if res, _ := c.Run(exec.Command{Argv: []string{"launchctl", "bootstrap", domain, path}, IgnoreExitCode: true}); res.Code != 0 {
		t.Fatalf("launchctl bootstrap %s: exit %d %s", domain, res.Code, res.Stderr)
	}

	pid := func() int {
		res, _ := c.Run(exec.Command{Argv: []string{"launchctl", "print", target}, IgnoreExitCode: true})
		for _, ln := range strings.Split(res.Stdout, "\n") {
			if f := strings.TrimSpace(ln); strings.HasPrefix(f, "pid = ") {
				n, _ := strconv.Atoi(strings.TrimPrefix(f, "pid = "))
				return n
			}
		}
		return 0
	}
	status := func() any {
		v, err := r.Exec.Call(c, "service.status", value.MapOf("name", label))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	settle := func(running bool) int {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if p := pid(); (p != 0) == running {
				return p
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("the agent did not reach running=%v", running)
		return 0
	}

	if status() != false {
		t.Errorf("service.status = %v for a loaded, idle agent", status())
	}

	if _, err := r.Exec.Call(c, "service.start", value.MapOf("name", label)); err != nil {
		t.Fatalf("service.start: %v", err)
	}
	first := pid()
	if first == 0 {
		t.Errorf("service.start returned and the agent has no pid")
		first = settle(true)
	}
	if status() != true {
		t.Errorf("service.status = %v while the agent runs as pid %d", status(), first)
	}

	if _, err := r.Exec.Call(c, "service.restart", value.MapOf("name", label)); err != nil {
		t.Fatalf("service.restart: %v", err)
	}
	if second := settle(true); second == first {
		t.Errorf("service.restart left pid %d in place", first)
	}

	// The state that was wrong: a running agent is not already dead.
	res, err := r.States.Call(c, "service.dead", value.MapOf("name", label))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || !res.HasChanges() {
		t.Errorf("service.dead on a running agent: %+v", res)
	}
	settle(false)
	if status() != false {
		t.Errorf("service.status = %v after the agent was stopped", status())
	}
	if again, _ := r.States.Call(c, "service.dead", value.MapOf("name", label)); again.HasChanges() {
		t.Errorf("service.dead on a stopped agent reported a change: %+v", again)
	}

	// The disable store the agent's own domain keeps, not the system one.
	disabled := func() string {
		res, _ := c.Run(exec.Command{Argv: []string{"launchctl", "print-disabled", domain}, IgnoreExitCode: true})
		for _, ln := range strings.Split(res.Stdout, "\n") {
			if strings.Contains(ln, `"`+label+`"`) {
				return strings.TrimSpace(ln)
			}
		}
		return ""
	}
	if _, err := r.Exec.Call(c, "service.disable", value.MapOf("name", label)); err != nil {
		t.Fatalf("service.disable: %v", err)
	}
	if got := disabled(); !strings.HasSuffix(got, "disabled") && !strings.HasSuffix(got, "true") {
		t.Errorf("after service.disable, %s's store says %q", domain, got)
	}
	if v, _ := r.Exec.Call(c, "service.enabled", value.MapOf("name", label)); v != false {
		t.Errorf("service.enabled = %v after disabling", v)
	}
	if _, err := r.Exec.Call(c, "service.enable", value.MapOf("name", label)); err != nil {
		t.Fatalf("service.enable: %v", err)
	}
	if got := disabled(); !strings.HasSuffix(got, "enabled") && !strings.HasSuffix(got, "false") {
		t.Errorf("after service.enable, %s's store says %q", domain, got)
	}
}
