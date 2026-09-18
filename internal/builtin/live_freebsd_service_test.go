package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The `service` module's **FreeBSD rc** provider, driven against a real
// `service(8)` and `sysrc(8)`.
//
// # What was assumed until now
//
// The mutating half, entirely. `evidence.go` said of this module that
// what is not covered includes "the FreeBSD rc branch, which still only
// reads" — and FreeBSD is SPEC 27.1 tier 1 and four of the five hosts
// this project's own fleet runs on.
//
// # What it brings with it
//
// An rc.d script of its own, `/usr/local/etc/rc.d/haliteprobe`, written
// the way rc.subr expects rather than the way a shell script would be:
// a `PROVIDE` line, an `rcvar`, a pidfile, and **an explicit
// `procname`**. That last one is not decoration. `command` here is
// `/usr/sbin/daemon`, and rc.subr checks a pidfile against `procname`,
// which defaults to `$command`; the pid `daemon -p` writes is the
// *child's*, so without `procname` the status check compares a `sleep`
// against `/usr/sbin/daemon`, finds no match, and reports a running
// service as stopped. DIVERGENCE 5.21 records exactly that trap against
// this project's own rc.d scripts -- "rc looked for `daemon` at a pid
// belonging to `halite-hub` and reported a running service as stopped"
// -- so this script was written from that row rather than by finding it
// again.
//
// # What is checked against what
//
// Never the module's own read-back: `sysrc -n haliteprobe_enable` for
// the boot state, and the pidfile plus `service haliteprobe status` run
// directly for whether it is up.
//
// # Why it skips everywhere else
//
// `HALITE_SYSTEM_LIVE=1` and root, the gate the rest of the FreeBSD row
// shares: this writes an rc.d script, edits rc.conf and starts a
// process. Run it:
//
//	sudo HALITE_SYSTEM_LIVE=1 go test -run TestLiveFreeBSDService -v ./internal/builtin/

const liveRCName = "haliteprobe"

const liveRCScriptDir = "/usr/local/etc/rc.d"

// The probe service. `daemon -p` writes the child's pid, so `procname`
// names the child rather than `daemon` itself — see the comment above.
const liveRCScript = `#!/bin/sh
#
# PROVIDE: ` + liveRCName + `
# REQUIRE: LOGIN
# KEYWORD: shutdown
#
# A throwaway service written by halite's live test. If this file is
# still here, the test that wrote it did not finish; it is safe to
# remove, and nothing starts it unless rc.conf says so.

. /etc/rc.subr

name="` + liveRCName + `"
rcvar="` + liveRCName + `_enable"
pidfile="/var/run/${name}.pid"
command="/usr/sbin/daemon"
procname="/bin/sleep"
command_args="-p ${pidfile} -f /bin/sleep 3600"

load_rc_config $name
: ${` + liveRCName + `_enable:="NO"}

run_rc_command "$1"
`

// freebsdServiceLive gates on a FreeBSD that has been offered up, and on
// the module actually choosing the provider this file is about.
func freebsdServiceLive(t *testing.T) *exec.Context {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this writes an rc.d script and edits rc.conf")
	}
	if runtime.GOOS != "freebsd" {
		t.Skipf("rc.d and sysrc are FreeBSD's, and this is %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_SYSTEM_LIVE is set on FreeBSD and this is not root; rc.conf and rc.d both need it")
	}
	c := realCtx(t)
	p, err := pickServiceProvider(c)
	if err != nil {
		t.Fatalf("no init system was recognised on this FreeBSD host: %v", err)
	}
	if p.Name() != "freebsd_service" {
		t.Fatalf("the service module picked the %s provider on FreeBSD; this file tests rc.d", p.Name())
	}
	return c
}

// installProbeRCScript writes the script and takes it, its rc.conf
// variable and anything it started back out afterwards.
func installProbeRCScript(t *testing.T, c *exec.Context) {
	t.Helper()
	if err := os.MkdirAll(liveRCScriptDir, 0o755); err != nil {
		t.Fatalf("the local rc.d directory could not be made: %v", err)
	}
	path := filepath.Join(liveRCScriptDir, liveRCName)
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("%s already exists; this test will not overwrite it", path)
	}
	if err := os.WriteFile(path, []byte(liveRCScript), 0o755); err != nil {
		t.Fatalf("writing the probe rc.d script: %v", err)
	}
	t.Cleanup(func() {
		for _, argv := range [][]string{
			{"service", liveRCName, "forcestop"},
			// -x removes the variable rather than setting it to NO, so
			// rc.conf goes back to not mentioning this service at all.
			{"sysrc", "-x", liveRCName + "_enable"},
		} {
			_, _ = c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		}
		_ = os.Remove(path)
		_ = os.Remove("/var/run/" + liveRCName + ".pid")
		res, _ := c.Run(exec.Command{
			Argv:           []string{"sysrc", "-n", liveRCName + "_enable"},
			IgnoreExitCode: true,
		})
		if res.Code == 0 {
			t.Errorf("rc.conf still carries %s_enable = %q after cleanup",
				liveRCName, strings.TrimSpace(res.Stdout))
		}
	})
}

// liveRCPID reads the pid the probe's pidfile holds, and 0 when nothing
// is running under it — the tool's own record, not the module's answer.
func liveRCPID(t *testing.T, c *exec.Context) int {
	t.Helper()
	raw, err := os.ReadFile("/var/run/" + liveRCName + ".pid")
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	// A pidfile outlives the process it named if a stop went wrong, so
	// the process decides rather than the file. `ps` rather than a
	// signal, so that this reads the same way as everything else here:
	// ask the system, do not infer.
	res, err := c.Run(exec.Command{
		Argv:           []string{"ps", "-p", strconv.Itoa(pid)},
		IgnoreExitCode: true,
	})
	if err != nil || res.Code != 0 {
		return 0
	}
	return pid
}

// liveRCStatus asks `service <name> status` directly.
func liveRCStatus(t *testing.T, c *exec.Context) (bool, string) {
	t.Helper()
	res, err := c.Run(exec.Command{
		Argv:           []string{"service", liveRCName, "status"},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("`service %s status`: %v", liveRCName, err)
	}
	return res.Code == 0, strings.TrimSpace(res.Stdout + res.Stderr)
}

// liveRCEnableVar reads rc.conf through sysrc, which is where the boot
// state actually lives.
func liveRCEnableVar(t *testing.T, c *exec.Context) (string, bool) {
	t.Helper()
	res, err := c.Run(exec.Command{
		Argv:           []string{"sysrc", "-n", liveRCName + "_enable"},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("sysrc: %v", err)
	}
	if res.Code != 0 {
		return "", false
	}
	return strings.TrimSpace(res.Stdout), true
}

// **The whole arc, in the order an SLS file would reach it.**
//
// rc.conf is the boot state and the rcvar is also a *permission*: an
// rc.d script with `rcvar` set refuses a plain `start` until rc.conf
// says YES. That is a FreeBSD-shaped fact with no systemd counterpart,
// and what `service.start` does in front of it is measured here rather
// than assumed.
func TestLiveFreeBSDServiceDrivesTheWholeArc(t *testing.T) {
	c := freebsdServiceLive(t)
	installProbeRCScript(t, c)
	r := New()

	if running, out := liveRCStatus(t, c); running {
		t.Fatalf("the probe is running before the test started: %s", out)
	}
	if status, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveRCName)); err != nil || status != false {
		t.Errorf("service.status = %v, %v on a service that is not running", status, err)
	}
	if enabled, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveRCName)); err != nil || enabled != false {
		t.Errorf("service.enabled = %v, %v on a service rc.conf does not mention", enabled, err)
	}

	// **A service rc.conf has not enabled still starts.** This is what
	// a tree that says `service.running` without `enable: true` gets,
	// and it is what found the defect: rc.subr refuses a plain `start`
	// for a service whose rcvar is not YES, and refuses it by exiting
	// 0, so halite reported a service started while nothing had
	// started. It is `service.running`'s meaning on every other
	// platform, and rc.subr's own `one`-prefixed verbs are how FreeBSD
	// spells it. DIVERGENCE 5.123.
	if _, err := r.Exec.Call(c, "service.start", value.MapOf("name", liveRCName)); err != nil {
		t.Fatalf("service.start on a service rc.conf has not enabled: %v", err)
	}
	disabledPID := waitForRCPID(t, c, true)
	if running, out := liveRCStatus(t, c); !running {
		t.Fatalf("service.start returned and `service %s status` says: %s", liveRCName, out)
	}
	t.Logf("service.start on a service rc.conf has not enabled: started it, pid %d", disabledPID)
	if status, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveRCName)); err != nil || status != true {
		t.Errorf("service.status = %v, %v while pid %d is running", status, err, disabledPID)
	}

	// And stopping one has the same shape: `stop` is refused for a
	// disabled service exactly as `start` is, so a `service.dead` on a
	// running-but-disabled service would have reported it stopped and
	// left it running.
	if _, err := r.Exec.Call(c, "service.stop", value.MapOf("name", liveRCName)); err != nil {
		t.Fatalf("service.stop on a service rc.conf has not enabled: %v", err)
	}
	waitForRCPID(t, c, false)
	if running, out := liveRCStatus(t, c); running {
		t.Fatalf("service.stop returned and `service %s status` says: %s", liveRCName, out)
	}

	// Enable, and check rc.conf itself rather than the module.
	if _, err := r.Exec.Call(c, "service.enable", value.MapOf("name", liveRCName)); err != nil {
		t.Fatalf("service.enable: %v", err)
	}
	enableVar, present := liveRCEnableVar(t, c)
	if !present || !strings.EqualFold(enableVar, "YES") {
		t.Errorf("rc.conf says %s_enable = %q (present: %v) after service.enable", liveRCName, enableVar, present)
	}
	if enabled, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveRCName)); err != nil || enabled != true {
		t.Errorf("service.enabled = %v, %v while rc.conf says %q", enabled, err, enableVar)
	}

	if _, err := r.Exec.Call(c, "service.start", value.MapOf("name", liveRCName)); err != nil {
		t.Fatalf("service.start on an enabled service: %v", err)
	}
	first := waitForRCPID(t, c, true)
	if running, out := liveRCStatus(t, c); !running {
		t.Fatalf("`service %s status` says it is not running after service.start: %s", liveRCName, out)
	}
	if status, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveRCName)); err != nil || status != true {
		t.Errorf("service.status = %v, %v while pid %d is running", status, err, first)
	}

	// A restart has to replace the process, not merely leave one
	// running: rc.subr's restart is stop-then-start, and a stop that
	// silently failed would leave the first pid in place.
	if _, err := r.Exec.Call(c, "service.restart", value.MapOf("name", liveRCName)); err != nil {
		t.Fatalf("service.restart: %v", err)
	}
	second := waitForRCPID(t, c, true)
	if second == first {
		t.Errorf("service.restart left pid %d in place; the service was never restarted", first)
	}

	if _, err := r.Exec.Call(c, "service.stop", value.MapOf("name", liveRCName)); err != nil {
		t.Fatalf("service.stop: %v", err)
	}
	waitForRCPID(t, c, false)
	if running, out := liveRCStatus(t, c); running {
		t.Errorf("`service %s status` still says it is running after service.stop: %s", liveRCName, out)
	}
	if status, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveRCName)); err != nil || status != false {
		t.Errorf("service.status = %v, %v after the service was stopped", status, err)
	}

	if _, err := r.Exec.Call(c, "service.disable", value.MapOf("name", liveRCName)); err != nil {
		t.Fatalf("service.disable: %v", err)
	}
	after, present := liveRCEnableVar(t, c)
	if !present || strings.EqualFold(after, "YES") {
		t.Errorf("rc.conf says %s_enable = %q (present: %v) after service.disable", liveRCName, after, present)
	}
	if enabled, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveRCName)); err != nil || enabled != false {
		t.Errorf("service.enabled = %v, %v while rc.conf says %q", enabled, err, after)
	}
}

// **get_all and available**, which on FreeBSD read `service -l`.
func TestLiveFreeBSDServiceListsTheProbe(t *testing.T) {
	c := freebsdServiceLive(t)
	installProbeRCScript(t, c)
	r := New()

	out, err := r.Exec.Call(c, "service.get_all", value.NewMap(0))
	if err != nil {
		t.Fatalf("service.get_all: %v", err)
	}
	names, _ := out.([]any)
	if len(names) < 20 {
		t.Fatalf("a FreeBSD host has more than %d rc.d scripts; the listing was not parsed", len(names))
	}
	found := false
	for _, n := range names {
		if s, _ := n.(string); s == liveRCName {
			found = true
		}
	}
	if !found {
		t.Errorf("the probe script is not in service.get_all")
	}

	if avail, err := r.Exec.Call(c, "service.available", value.MapOf("name", liveRCName)); err != nil || avail != true {
		t.Errorf("service.available = %v, %v for a script in the local rc.d directory", avail, err)
	}
	if missing, err := r.Exec.Call(c, "service.missing", value.MapOf("name", "halite-no-such-service")); err != nil || missing != true {
		t.Errorf("service.missing = %v, %v for a script that is not there", missing, err)
	}
}

// **Masking is systemd's alone**, and the refusal names this init
// system rather than reporting a FreeBSD service as unmasked.
func TestLiveFreeBSDServiceRefusesToMask(t *testing.T) {
	c := freebsdServiceLive(t)
	r := New()

	_, err := r.Exec.Call(c, "service.masked", value.MapOf("name", liveRCName))
	if err == nil {
		t.Fatal("service.masked answered on rc.d, which has no such concept")
	}
	if !strings.Contains(err.Error(), "freebsd_service") {
		t.Errorf("the refusal does not name the init system: %v", err)
	}
}

// waitForRCPID polls the pidfile — not the module — until the service is
// up or down, and fails naming what `service status` said. rc.subr's
// start returns when `daemon` has forked, which is not quite when the
// pidfile is written.
func waitForRCPID(t *testing.T, c *exec.Context, wantRunning bool) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pid := liveRCPID(t, c)
		if (pid != 0) == wantRunning {
			return pid
		}
		if time.Now().After(deadline) {
			state := "running"
			if !wantRunning {
				state = "stopped"
			}
			_, out := liveRCStatus(t, c)
			t.Fatalf("the probe never became %s within thirty seconds; `service %s status` says: %s",
				state, liveRCName, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
