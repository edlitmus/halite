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

// waitForLaunchdPID polls the tool — not the module — until the job is
// running or is not, and returns the pid it settled on. launchd's
// `start` and `stop` are asynchronous: they ask, and the answer arrives
// when the process has actually been spawned or reaped.
func waitForLaunchdPID(t *testing.T, c *exec.Context, wantRunning bool) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		pid, out := launchdRunningPID(t, c)
		if (pid != 0) == wantRunning {
			return pid
		}
		if time.Now().After(deadline) {
			state := "running"
			if !wantRunning {
				state = "stopped"
			}
			t.Fatalf("the probe daemon never became %s within ten seconds; `launchctl print` says:\n%s", state, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
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

	if pid, out := launchdRunningPID(t, c); pid != 0 {
		t.Fatalf("the probe daemon started itself at load; RunAtLoad is false:\n%s", out)
	}
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
func TestLiveMacServiceStartsStopsAndRestarts(t *testing.T) {
	c := launchdLive(t)
	installProbeDaemon(t, c)
	r := New()

	if _, err := r.Exec.Call(c, "service.start", value.MapOf("name", liveLaunchdLabel)); err != nil {
		t.Fatalf("service.start: %v", err)
	}
	// Measured rather than asserted: unlike the systemd provider, which
	// awaits `JobRemoved`, launchd's `start` returns as soon as the
	// request is queued. What this run saw is worth reporting either
	// way; what the test then asserts is the settled state.
	if immediate, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveLaunchdLabel)); err == nil {
		t.Logf("service.status the instant service.start returned: %v", immediate)
	}
	first := waitForLaunchdPID(t, c, true)

	running, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveLaunchdLabel))
	if err != nil {
		t.Fatalf("service.status: %v", err)
	}
	if running != true {
		_, out := launchdRunningPID(t, c)
		t.Errorf("service.status = %v while pid %d is running:\n%s", running, first, out)
	}

	if _, err := r.Exec.Call(c, "service.restart", value.MapOf("name", liveLaunchdLabel)); err != nil {
		t.Fatalf("service.restart on a running job: %v", err)
	}
	second := waitForLaunchdPID(t, c, true)
	if second == first {
		t.Errorf("service.restart left pid %d in place; the job was never restarted", first)
	}

	if _, err := r.Exec.Call(c, "service.stop", value.MapOf("name", liveLaunchdLabel)); err != nil {
		t.Fatalf("service.stop: %v", err)
	}
	waitForLaunchdPID(t, c, false)
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
	waitForLaunchdPID(t, c, true)
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
