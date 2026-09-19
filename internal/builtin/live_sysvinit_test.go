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

// The `service` module's **sysvinit** provider, driven against a real
// sysvinit.
//
// # What was here before
//
// Nothing had run it. `evidence.go` said so in those words, and it was
// the last of the four providers in that state: launchd closed on the
// macOS leg (DIVERGENCE 5.122), OpenRC on the lab's Alpine row (5.124),
// and this one waited for a machine with sysvinit as PID 1 — which
// plan.md §7 item 17 described as "a row that does not exist yet".
//
// # The machine
//
// Debian with `sysvinit-core` in place of `systemd-sysv`, which is what
// item 17 named when it turned out there is no Devuan in Vultr's
// catalogue. `contrib/tofu`'s `debian13sysv` row makes one.
//
// **systemd is still installed there and `systemctl` is still on
// PATH** — only PID 1 has changed. That is not an accident of the
// conversion, it is the interesting part: `systemdProvider.Available`
// tests for `/run/systemd/system` precisely because the binary being
// present says nothing, and until this machine existed nothing had ever
// exercised the branch where that test is what decides.
//
// # What it brings with it
//
// An LSB init script of its own, `/etc/init.d/haliteprobe`, driven by
// `start-stop-daemon` the way Debian's own scripts are, with the
// `### BEGIN INIT INFO` block `update-rc.d` needs to place its links.
//
// # What is checked against what
//
// Never the module's own read-back: the pidfile and `service <name>
// status` for whether it is up, and the runlevel directories on disk for
// the boot state, which is what `update-rc.d` actually writes.
//
// # Why it skips everywhere else
//
// `HALITE_SYSTEM_LIVE=1` and root: it writes an init script, rewrites
// runlevel links and starts a process.

const liveSysvName = "haliteprobe"

const liveSysvScriptPath = "/etc/init.d/" + liveSysvName

// The probe service. `start-stop-daemon --background --make-pidfile` is
// how Debian's own scripts daemonise a program that does not daemonise
// itself, and `--status`'s exit codes are the ones `service ... status`
// is expected to pass through.
const liveSysvScript = `#!/bin/sh
### BEGIN INIT INFO
# Provides:          ` + liveSysvName + `
# Required-Start:    $remote_fs $syslog
# Required-Stop:     $remote_fs $syslog
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: halite live-test probe
# Description:       A throwaway service written by halite's live test.
### END INIT INFO

PIDFILE=/run/` + liveSysvName + `.pid

case "$1" in
  start)
    start-stop-daemon --start --quiet --background \
        --make-pidfile --pidfile "$PIDFILE" \
        --exec /bin/sleep -- 3600
    ;;
  stop)
    start-stop-daemon --stop --quiet --retry 5 --pidfile "$PIDFILE"
    rm -f "$PIDFILE"
    ;;
  restart|force-reload)
    $0 stop
    $0 start
    ;;
  status)
    start-stop-daemon --status --pidfile "$PIDFILE" && exit 0 || exit 3
    ;;
  *)
    echo "usage: $0 {start|stop|restart|status}" >&2
    exit 2
    ;;
esac
exit 0
`

// sysvinitLive gates on a machine that has been offered up and is really
// running sysvinit, and on the module choosing the provider this file is
// about.
func sysvinitLive(t *testing.T) *exec.Context {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this writes an init script and rewrites runlevel links")
	}
	if runtime.GOOS != "linux" {
		t.Skipf("sysvinit here is Linux's, and this is %s", runtime.GOOS)
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		t.Skip("systemd is running on this machine; the sysvinit provider is not what it would use")
	}
	c := realCtx(t)
	if c.Which("service") == "" || c.Which("update-rc.d") == "" {
		t.Skip("this host has no `service` and `update-rc.d`; the lab's debian13sysv row is where this leg belongs")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_SYSTEM_LIVE is set on a sysvinit host and this is not root; init scripts and runlevel links both need it")
	}
	p, err := pickServiceProvider(c)
	if err != nil {
		t.Fatalf("no init system was recognised on this sysvinit host: %v", err)
	}
	if p.Name() != "sysvinit_service" {
		t.Fatalf("the service module picked the %s provider on a sysvinit host; this file tests sysvinit", p.Name())
	}
	return c
}

// installProbeSysvScript writes the script and takes it, its runlevel
// links and anything it started back out afterwards.
func installProbeSysvScript(t *testing.T, c *exec.Context) {
	t.Helper()
	if _, err := os.Stat(liveSysvScriptPath); err == nil {
		t.Fatalf("%s already exists; this test will not overwrite it", liveSysvScriptPath)
	}
	if err := os.WriteFile(liveSysvScriptPath, []byte(liveSysvScript), 0o755); err != nil {
		t.Fatalf("writing the probe init script: %v", err)
	}
	t.Cleanup(func() {
		for _, argv := range [][]string{
			{"service", liveSysvName, "stop"},
			// `remove` takes the links out of every runlevel; `disable`
			// would only turn them into K links and leave the machine
			// carrying a service it never had.
			{"update-rc.d", "-f", liveSysvName, "remove"},
		} {
			_, _ = c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		}
		_ = os.Remove(liveSysvScriptPath)
		_ = os.Remove("/run/" + liveSysvName + ".pid")
		if links := sysvRunlevelLinks(t, liveSysvName); len(links) > 0 {
			t.Errorf("the probe still has runlevel links after cleanup: %v", links)
		}
	})
}

// sysvRunlevelLinks reads what is actually on disk: every rc?.d entry
// that points at this service, with the S or K prefix that decides
// whether it starts or stops there.
//
// Deliberately not `update-rc.d`'s own output — it has none to speak of
// — and deliberately not this module's reader, which looks at one
// runlevel.
func sysvRunlevelLinks(t *testing.T, name string) []string {
	t.Helper()
	var out []string
	for _, level := range []string{"0", "1", "2", "3", "4", "5", "6", "S"} {
		dir := "/etc/rc" + level + ".d"
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), name) {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	return out
}

// sysvStartLinks is the subset that starts the service at boot.
func sysvStartLinks(t *testing.T, name string) []string {
	t.Helper()
	var out []string
	for _, link := range sysvRunlevelLinks(t, name) {
		if strings.HasPrefix(filepath.Base(link), "S") {
			out = append(out, link)
		}
	}
	return out
}

// liveSysvPID reads the pid the probe's pidfile holds, and 0 when
// nothing is running under it.
func liveSysvPID(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile("/run/" + liveSysvName + ".pid")
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err != nil {
		return 0
	}
	return pid
}

// liveSysvStatus asks `service <name> status` directly.
func liveSysvStatus(t *testing.T, c *exec.Context) (bool, string) {
	t.Helper()
	res, err := c.Run(exec.Command{
		Argv:           []string{"service", liveSysvName, "status"},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("`service %s status`: %v", liveSysvName, err)
	}
	return res.Code == 0, strings.TrimSpace(res.Stdout + res.Stderr)
}

// waitForSysvPID polls the pidfile — not the module — until the service
// is up or down.
func waitForSysvPID(t *testing.T, c *exec.Context, wantRunning bool) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pid := liveSysvPID(t)
		if (pid != 0) == wantRunning {
			return pid
		}
		if time.Now().After(deadline) {
			state := "running"
			if !wantRunning {
				state = "stopped"
			}
			_, out := liveSysvStatus(t, c)
			t.Fatalf("the probe never became %s within thirty seconds; `service %s status` says: %s",
				state, liveSysvName, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// **The whole arc, in the order an SLS file would reach it.**
func TestLiveSysvinitServiceDrivesTheWholeArc(t *testing.T) {
	c := sysvinitLive(t)
	installProbeSysvScript(t, c)
	r := New()

	if running, out := liveSysvStatus(t, c); running {
		t.Fatalf("the probe is running before the test started: %s", out)
	}
	if status, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveSysvName)); err != nil || status != false {
		t.Errorf("service.status = %v, %v on a service that is not running", status, err)
	}
	if enabled, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveSysvName)); err != nil || enabled != false {
		t.Errorf("service.enabled = %v, %v on a service with no runlevel links", enabled, err)
	}

	if _, err := r.Exec.Call(c, "service.start", value.MapOf("name", liveSysvName)); err != nil {
		t.Fatalf("service.start: %v", err)
	}
	first := waitForSysvPID(t, c, true)
	if running, out := liveSysvStatus(t, c); !running {
		t.Fatalf("service.start returned and `service %s status` says: %s", liveSysvName, out)
	}
	if status, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveSysvName)); err != nil || status != true {
		t.Errorf("service.status = %v, %v while pid %d is running", status, err, first)
	}

	// A restart has to replace the process rather than leave one
	// running: a stop that silently failed would keep the first pid.
	if _, err := r.Exec.Call(c, "service.restart", value.MapOf("name", liveSysvName)); err != nil {
		t.Fatalf("service.restart: %v", err)
	}
	second := waitForSysvPID(t, c, true)
	if second == first {
		t.Errorf("service.restart left pid %d in place; the service was never restarted", first)
	}

	if _, err := r.Exec.Call(c, "service.stop", value.MapOf("name", liveSysvName)); err != nil {
		t.Fatalf("service.stop: %v", err)
	}
	waitForSysvPID(t, c, false)
	if running, out := liveSysvStatus(t, c); running {
		t.Errorf("`service %s status` still says it is running after service.stop: %s", liveSysvName, out)
	}

	// The boot state, against the links on disk rather than the
	// module's own reader.
	if _, err := r.Exec.Call(c, "service.enable", value.MapOf("name", liveSysvName)); err != nil {
		t.Fatalf("service.enable: %v", err)
	}
	starts := sysvStartLinks(t, liveSysvName)
	if len(starts) == 0 {
		t.Fatalf("service.enable wrote no start links; the runlevel entries are %v",
			sysvRunlevelLinks(t, liveSysvName))
	}
	t.Logf("after service.enable the probe starts in %v", starts)
	if enabled, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveSysvName)); err != nil || enabled != true {
		t.Errorf("service.enabled = %v, %v while the links on disk are %v", enabled, err, starts)
	}

	if _, err := r.Exec.Call(c, "service.disable", value.MapOf("name", liveSysvName)); err != nil {
		t.Fatalf("service.disable: %v", err)
	}
	after := sysvStartLinks(t, liveSysvName)
	t.Logf("after service.disable the runlevel entries are %v", sysvRunlevelLinks(t, liveSysvName))
	if len(after) != 0 {
		t.Errorf("service.disable left start links behind: %v", after)
	}
	if enabled, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveSysvName)); err != nil || enabled != false {
		t.Errorf("service.enabled = %v, %v after service.disable", enabled, err)
	}
}

// **The systemd provider declines a machine that still has
// `systemctl`.**
//
// Converting a Debian to sysvinit leaves every systemd binary in place;
// only PID 1 changes. `systemdProvider.Available` has always tested for
// `/run/systemd/system` as well as for the binary, with a comment saying
// a container image often carries one without the other — and until this
// row existed, nothing had run on a machine where that test is what
// decides.
func TestLiveSysvinitIsPickedWhileSystemctlIsStillInstalled(t *testing.T) {
	c := sysvinitLive(t)

	if c.Which("systemctl") == "" {
		t.Skip("this machine has no systemctl at all, so the check this test is about does not arise")
	}
	t.Log("systemctl is installed on this machine and systemd is not running; /run/systemd/system is what decides")

	if (systemdProvider{}).Available(c) {
		t.Error("the systemd provider offered to drive a machine where systemd is not PID 1")
	}
	p, err := pickServiceProvider(c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "sysvinit_service" {
		t.Errorf("the %s provider was picked on a sysvinit host", p.Name())
	}
}

// **get_all and available**, which on sysvinit read /etc/init.d.
func TestLiveSysvinitServiceListsTheProbe(t *testing.T) {
	c := sysvinitLive(t)
	installProbeSysvScript(t, c)
	r := New()

	out, err := r.Exec.Call(c, "service.get_all", value.NewMap(0))
	if err != nil {
		t.Fatalf("service.get_all: %v", err)
	}
	names, _ := out.([]any)
	if len(names) < 5 {
		t.Fatalf("a Debian has more than %d init scripts; the listing was not parsed", len(names))
	}
	found := false
	for _, n := range names {
		if s, _ := n.(string); s == liveSysvName {
			found = true
		}
	}
	if !found {
		t.Errorf("the probe script is not in service.get_all: %v", names)
	}
	if avail, err := r.Exec.Call(c, "service.available", value.MapOf("name", liveSysvName)); err != nil || avail != true {
		t.Errorf("service.available = %v, %v for a script in /etc/init.d", avail, err)
	}
	if missing, err := r.Exec.Call(c, "service.missing", value.MapOf("name", "halite-no-such-service")); err != nil || missing != true {
		t.Errorf("service.missing = %v, %v for a script that is not there", missing, err)
	}
}

// **Masking is systemd's alone**, and the refusal names this init
// system.
func TestLiveSysvinitServiceRefusesToMask(t *testing.T) {
	c := sysvinitLive(t)
	r := New()

	_, err := r.Exec.Call(c, "service.masked", value.MapOf("name", liveSysvName))
	if err == nil {
		t.Fatal("service.masked answered on sysvinit, which has no such concept")
	}
	if !strings.Contains(err.Error(), "sysvinit_service") {
		t.Errorf("the refusal does not name the init system: %v", err)
	}
}
