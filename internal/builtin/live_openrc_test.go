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

// The `service` module's **OpenRC** provider, driven against a real
// `rc-service` and `rc-update`.
//
// # What was here before
//
// No provider at all. OpenRC keeps its init scripts in /etc/init.d,
// which is what the sysvinit provider looks for, so an Alpine node
// either reached that provider — whose `update-rc.d` and `chkconfig`
// are Debian's and RedHat's and exist on neither Alpine nor Gentoo — or
// matched nothing and failed every `service.*` call with "no init system
// was recognised on this node". plan.md §7 item 16 is where this was
// ranked, and `evidence.go` said the openrc provider "has not been run
// at all", which was true in a stronger sense than it read.
//
// # What it brings with it
//
// An OpenRC init script of its own, `/etc/init.d/haliteprobe`, using
// `supervise-daemon`'s simpler ancestor: `command`, `command_args`,
// `command_background` and a pidfile, which is the shape every Alpine
// service script has.
//
// # What is checked against what
//
// Never the module's own read-back: `rc-update show` for the runlevels,
// the pidfile and `rc-service <name> status` run directly for whether it
// is up.
//
// # Why it skips everywhere else
//
// `HALITE_SYSTEM_LIVE=1` and root: this writes an init script, edits
// runlevels and starts a process. The lab's Alpine row is where it runs
// (`make lab-up LAB_DISTROS='["alpine"]'`, then `make lab-test`).

const liveOpenRCName = "haliteprobe"

const liveOpenRCScriptPath = "/etc/init.d/" + liveOpenRCName

// The probe service, in the shape Alpine's own scripts use.
const liveOpenRCScript = `#!/sbin/openrc-run
# A throwaway service written by halite's live test. If this file is
# still here, the test that wrote it did not finish; it is safe to
# remove, and nothing starts it unless a runlevel says so.

name="` + liveOpenRCName + `"
description="halite live-test probe"
command="/bin/sleep"
command_args="3600"
command_background="yes"
pidfile="/run/${RC_SVCNAME}.pid"
`

// openrcLive gates on a machine that has been offered up, and on the
// module actually choosing the provider this file is about.
func openrcLive(t *testing.T) *exec.Context {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this writes an init script and edits runlevels")
	}
	if runtime.GOOS != "linux" {
		t.Skipf("OpenRC is Linux's, and this is %s", runtime.GOOS)
	}
	c := realCtx(t)
	if c.Which("rc-service") == "" || c.Which("rc-update") == "" {
		t.Skip("this host does not run OpenRC; the lab's Alpine row is where this leg belongs")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_SYSTEM_LIVE is set on an OpenRC host and this is not root; runlevels and init.d both need it")
	}
	p, err := pickServiceProvider(c)
	if err != nil {
		t.Fatalf("no init system was recognised on this OpenRC host: %v", err)
	}
	if p.Name() != "openrc_service" {
		t.Fatalf("the service module picked the %s provider on an OpenRC host; this file tests OpenRC", p.Name())
	}
	return c
}

// installProbeOpenRCScript writes the script and takes it, its runlevels
// and anything it started back out afterwards.
func installProbeOpenRCScript(t *testing.T, c *exec.Context) {
	t.Helper()
	if _, err := os.Stat(liveOpenRCScriptPath); err == nil {
		t.Fatalf("%s already exists; this test will not overwrite it", liveOpenRCScriptPath)
	}
	if err := os.WriteFile(liveOpenRCScriptPath, []byte(liveOpenRCScript), 0o755); err != nil {
		t.Fatalf("writing the probe init script: %v", err)
	}
	t.Cleanup(func() {
		_, _ = c.Run(exec.Command{
			Argv:           []string{"rc-service", liveOpenRCName, "stop"},
			IgnoreExitCode: true,
		})
		levels, _ := openrcRunlevels(c, liveOpenRCName)
		for _, level := range levels {
			_, _ = c.Run(exec.Command{
				Argv:           []string{"rc-update", "del", liveOpenRCName, level},
				IgnoreExitCode: true,
			})
		}
		_ = os.Remove(liveOpenRCScriptPath)
		_ = os.Remove("/run/" + liveOpenRCName + ".pid")
		if left, _ := openrcRunlevels(c, liveOpenRCName); len(left) > 0 {
			t.Errorf("the probe is still in runlevels %v after cleanup", left)
		}
	})
}

// liveOpenRCPID reads the pid the probe's pidfile holds, and 0 when
// nothing is running under it.
func liveOpenRCPID(t *testing.T, c *exec.Context) int {
	t.Helper()
	raw, err := os.ReadFile("/run/" + liveOpenRCName + ".pid")
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	// `/proc/<pid>` rather than `ps -p`, and this is the second version
	// of this helper: Alpine's `ps` is busybox's, which has no `-p` at
	// all and exits 1 saying "unrecognized option". The first run of
	// this test failed for thirty seconds on that while `rc-service
	// status` was cheerfully reporting "started" -- the probe was
	// wrong, not the machine.
	if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err != nil {
		return 0
	}
	return pid
}

// liveOpenRCStatus asks `rc-service <name> status` directly.
func liveOpenRCStatus(t *testing.T, c *exec.Context) (bool, string) {
	t.Helper()
	res, err := c.Run(exec.Command{
		Argv:           []string{"rc-service", liveOpenRCName, "status"},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("`rc-service %s status`: %v", liveOpenRCName, err)
	}
	return res.Code == 0, strings.TrimSpace(res.Stdout + res.Stderr)
}

// waitForOpenRCPID polls the pidfile — not the module — until the
// service is up or down.
func waitForOpenRCPID(t *testing.T, c *exec.Context, wantRunning bool) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pid := liveOpenRCPID(t, c)
		if (pid != 0) == wantRunning {
			return pid
		}
		if time.Now().After(deadline) {
			state := "running"
			if !wantRunning {
				state = "stopped"
			}
			_, out := liveOpenRCStatus(t, c)
			t.Fatalf("the probe never became %s within thirty seconds; `rc-service %s status` says: %s",
				state, liveOpenRCName, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// **The whole arc, in the order an SLS file would reach it.**
func TestLiveOpenRCServiceDrivesTheWholeArc(t *testing.T) {
	c := openrcLive(t)
	installProbeOpenRCScript(t, c)
	r := New()

	// The raw listing this provider's runlevel reader parses, logged
	// once: a claim about another program's output is worth having that
	// program's own words beside it.
	if res, err := c.Run(exec.Command{Argv: []string{"rc-update", "show"}, IgnoreExitCode: true}); err == nil {
		t.Logf("`rc-update show` on this host:\n%s", res.Stdout)
	}

	if running, out := liveOpenRCStatus(t, c); running {
		t.Fatalf("the probe is running before the test started: %s", out)
	}
	if status, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveOpenRCName)); err != nil || status != false {
		t.Errorf("service.status = %v, %v on a service that is not running", status, err)
	}
	if enabled, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveOpenRCName)); err != nil || enabled != false {
		t.Errorf("service.enabled = %v, %v on a service in no runlevel", enabled, err)
	}

	if _, err := r.Exec.Call(c, "service.start", value.MapOf("name", liveOpenRCName)); err != nil {
		t.Fatalf("service.start: %v", err)
	}
	first := waitForOpenRCPID(t, c, true)
	if running, out := liveOpenRCStatus(t, c); !running {
		t.Fatalf("service.start returned and `rc-service %s status` says: %s", liveOpenRCName, out)
	}
	if status, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveOpenRCName)); err != nil || status != true {
		t.Errorf("service.status = %v, %v while pid %d is running", status, err, first)
	}

	// A restart has to replace the process rather than leave one
	// running: a stop that silently failed would keep the first pid.
	if _, err := r.Exec.Call(c, "service.restart", value.MapOf("name", liveOpenRCName)); err != nil {
		t.Fatalf("service.restart: %v", err)
	}
	second := waitForOpenRCPID(t, c, true)
	if second == first {
		t.Errorf("service.restart left pid %d in place; the service was never restarted", first)
	}

	if _, err := r.Exec.Call(c, "service.stop", value.MapOf("name", liveOpenRCName)); err != nil {
		t.Fatalf("service.stop: %v", err)
	}
	waitForOpenRCPID(t, c, false)
	if running, out := liveOpenRCStatus(t, c); running {
		t.Errorf("`rc-service %s status` still says it is running after service.stop: %s", liveOpenRCName, out)
	}

	// The boot state, against `rc-update`'s own answer.
	if _, err := r.Exec.Call(c, "service.enable", value.MapOf("name", liveOpenRCName)); err != nil {
		t.Fatalf("service.enable: %v", err)
	}
	levels, err := openrcRunlevels(c, liveOpenRCName)
	if err != nil {
		t.Fatalf("reading the runlevels: %v", err)
	}
	if len(levels) == 0 {
		t.Fatal("service.enable put the probe in no runlevel at all")
	}
	t.Logf("after service.enable the probe is in %v", levels)
	if enabled, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveOpenRCName)); err != nil || enabled != true {
		t.Errorf("service.enabled = %v, %v while rc-update says %v", enabled, err, levels)
	}

	if _, err := r.Exec.Call(c, "service.disable", value.MapOf("name", liveOpenRCName)); err != nil {
		t.Fatalf("service.disable: %v", err)
	}
	after, err := openrcRunlevels(c, liveOpenRCName)
	if err != nil {
		t.Fatalf("reading the runlevels back: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("service.disable left the probe in %v", after)
	}
	if enabled, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveOpenRCName)); err != nil || enabled != false {
		t.Errorf("service.enabled = %v, %v after service.disable", enabled, err)
	}
}

// **Disable takes a service out of every runlevel**, not only the one
// Enable puts it in. A service in `boot` starts at boot as surely as one
// in `default`, so a disable that only looked at `default` would report
// success and change nothing.
func TestLiveOpenRCServiceDisableLeavesNoRunlevelBehind(t *testing.T) {
	c := openrcLive(t)
	installProbeOpenRCScript(t, c)
	r := New()

	for _, level := range []string{"default", "boot"} {
		res, err := c.Run(exec.Command{
			Argv:           []string{"rc-update", "add", liveOpenRCName, level},
			IgnoreExitCode: true,
		})
		if err != nil || res.Code != 0 {
			t.Skipf("the probe could not be added to the %s runlevel: %v (%s)", level, err, res.Stderr)
		}
	}
	before, err := openrcRunlevels(c, liveOpenRCName)
	if err != nil || len(before) < 2 {
		t.Fatalf("the probe is in %v, and this test needs it in two runlevels (%v)", before, err)
	}

	if _, err := r.Exec.Call(c, "service.disable", value.MapOf("name", liveOpenRCName)); err != nil {
		t.Fatalf("service.disable: %v", err)
	}
	after, err := openrcRunlevels(c, liveOpenRCName)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("service.disable left the probe in %v, having found it in %v", after, before)
	}
}

// **OpenRC is not the sysvinit provider with different words**, and this
// machine is the proof rather than the argument.
//
// Alpine has `/etc/init.d` *and* a `/sbin/service` shim, which is
// exactly what `sysvProvider.Available` looks for -- so before this
// provider existed, `pickServiceProvider` picked sysvinit here. Start
// and stop happened to work through the shim. The boot state did not:
// there is no `update-rc.d`, no `chkconfig` and no `/etc/rc3.d` on this
// host, so `service.enabled` failed outright and `service.enable` ran a
// program that is not installed. DIVERGENCE 5.124.
func TestLiveOpenRCIsPickedOverSysvinitWhichWouldMatchToo(t *testing.T) {
	c := openrcLive(t)

	if !(sysvProvider{}).Available(c) {
		t.Skip("the sysvinit provider does not match this host, so the ordering this test is about does not arise")
	}
	t.Log("the sysvinit provider matches this host too; the provider order is what decides")

	p, err := pickServiceProvider(c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "openrc_service" {
		t.Fatalf("the %s provider was picked on an OpenRC host", p.Name())
	}

	// What a node used to get, measured rather than described: the boot
	// state through the provider that would have answered.
	if _, err := (sysvProvider{}).Enabled(c, "sshd"); err == nil {
		t.Error("the sysvinit provider answered the boot state on an OpenRC host; " +
			"this test's account of what was broken is wrong and the ledger should be corrected")
	} else {
		t.Logf("the sysvinit provider on this host answers the boot state with: %v", err)
	}
}

// **get_all and available**, which on OpenRC read `rc-service --list`.
func TestLiveOpenRCServiceListsTheProbe(t *testing.T) {
	c := openrcLive(t)
	installProbeOpenRCScript(t, c)
	r := New()

	out, err := r.Exec.Call(c, "service.get_all", value.NewMap(0))
	if err != nil {
		t.Fatalf("service.get_all: %v", err)
	}
	names, _ := out.([]any)
	if len(names) < 10 {
		t.Fatalf("an OpenRC host has more than %d init scripts; the listing was not parsed", len(names))
	}
	found := false
	for _, n := range names {
		if s, _ := n.(string); s == liveOpenRCName {
			found = true
		}
	}
	if !found {
		t.Errorf("the probe script is not in service.get_all: %v", names)
	}
	if avail, err := r.Exec.Call(c, "service.available", value.MapOf("name", liveOpenRCName)); err != nil || avail != true {
		t.Errorf("service.available = %v, %v for a script in /etc/init.d", avail, err)
	}
	if missing, err := r.Exec.Call(c, "service.missing", value.MapOf("name", "halite-no-such-service")); err != nil || missing != true {
		t.Errorf("service.missing = %v, %v for a script that is not there", missing, err)
	}
}

// **Masking is systemd's alone**, and the refusal names this init
// system.
func TestLiveOpenRCServiceRefusesToMask(t *testing.T) {
	c := openrcLive(t)
	r := New()

	_, err := r.Exec.Call(c, "service.masked", value.MapOf("name", liveOpenRCName))
	if err == nil {
		t.Fatal("service.masked answered on OpenRC, which has no such concept")
	}
	if !strings.Contains(err.Error(), "openrc_service") {
		t.Errorf("the refusal does not name the init system: %v", err)
	}
}
