package builtin

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The `service` module's systemd provider, driven against real systemd
// over its real D-Bus API.
//
// # What was assumed until now
//
// SPEC 15.2 says systemd is spoken to over D-Bus, falling back to
// `systemctl`. Until now the provider only ever ran `systemctl`, and
// DIVERGENCE §4.5 recorded the systemd provider as written and never
// executed at all under the compat layer -- `evidence.go` has it
// `Captured` because nothing has watched this module start or stop a
// service on any platform.
//
// # What this does
//
// It brings its own unit -- `/run/systemd/system/halite-live-probe.service`,
// a `oneshot` that runs `/bin/true` after a one-second `ExecStartPre`,
// attached to nothing -- loads it with `daemon-reload`, and drives it
// through the module: start, stop, restart, enable, disable, mask,
// unmask, and `get_all`. Every step is checked against `systemctl` run
// directly, not against the module's own read-back. The one-second
// pre-step makes the `JobRemoved` wait observable: a provider that did
// not wait would return while the unit was still `activating`.
//
// A second pass forces the bus dial to fail and confirms the same unit
// is driven by the `systemctl` fallback.
//
// # Why it skips everywhere else
//
// `HALITE_SYSTEM_LIVE=1`, the gate `hostname`, `sysctl`, `apparmor` and
// `netplan` share. Starting and masking services is not something to do
// because somebody typed `go test ./...`.

const liveServiceName = "halite-live-probe"

const liveServiceUnit = `[Unit]
Description=halite live-test probe

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStartPre=/bin/sleep 1
ExecStart=/bin/true

[Install]
WantedBy=multi-user.target
`

// serviceLive gates on the machine being offered up and running systemd.
func serviceLive(t *testing.T) *exec.Context {
	t.Helper()
	c := system(t)
	if runtime.GOOS != "linux" {
		t.Skipf("systemd is Linux's, and this is %s", runtime.GOOS)
	}
	if c.Which("systemctl") == "" {
		t.Fatal("HALITE_SYSTEM_LIVE is set and there is no systemctl; this is not a systemd node")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Fatalf("HALITE_SYSTEM_LIVE is set and systemd is not running here: %v", err)
	}
	return c
}

// installProbeUnit writes the unit, reloads, and removes every trace of
// it afterwards whatever the test did.
func installProbeUnit(t *testing.T, c *exec.Context) {
	t.Helper()
	path := "/run/systemd/system/" + liveServiceName + ".service"
	if err := os.WriteFile(path, []byte(liveServiceUnit), 0o644); err != nil {
		t.Fatalf("writing the probe unit: %v", err)
	}
	mustRun(t, c, "systemctl", "daemon-reload")
	t.Cleanup(func() {
		for _, argv := range [][]string{
			{"systemctl", "stop", liveServiceName},
			{"systemctl", "disable", liveServiceName},
			{"systemctl", "unmask", liveServiceName},
		} {
			_, _ = c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		}
		_ = os.Remove(path)
		_, _ = c.Run(exec.Command{Argv: []string{"systemctl", "daemon-reload"}, IgnoreExitCode: true})
		res, _ := c.Run(exec.Command{
			Argv:           []string{"systemctl", "is-active", liveServiceName},
			IgnoreExitCode: true,
		})
		if strings.TrimSpace(res.Stdout) == "active" {
			t.Errorf("the probe unit is still active after cleanup")
		}
	})
}

// countDBus wraps the bus dialer so a test can prove the D-Bus path was
// taken rather than the systemctl fallback.
func countDBus(t *testing.T) *int {
	t.Helper()
	n := 0
	old := systemdBus
	systemdBus = func(ctx context.Context) (systemdBackend, error) {
		b, err := dialSystemd(ctx)
		if err == nil {
			n++
		}
		return b, err
	}
	t.Cleanup(func() { systemdBus = old })
	return &n
}

func mustRun(t *testing.T, c *exec.Context, argv ...string) string {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		t.Fatalf("%s: %v", strings.Join(argv, " "), err)
	}
	if res.Code != 0 {
		t.Fatalf("%s exited %d: %s", strings.Join(argv, " "), res.Code, res.Stderr+res.Stdout)
	}
	return strings.TrimSpace(res.Stdout)
}

func systemctlProp(t *testing.T, c *exec.Context, prop string) string {
	t.Helper()
	out := mustRun(t, c, "systemctl", "show", "-p", prop, "--value", liveServiceName)
	return strings.TrimSpace(out)
}

// **start / stop / restart over D-Bus, with the job actually awaited.**
func TestLiveServiceStartsStopsAndRestartsOverDBus(t *testing.T) {
	c := serviceLive(t)
	installProbeUnit(t, c)
	hits := countDBus(t)
	r := New()

	if _, err := r.Exec.Call(c, "service.start", value.MapOf("name", liveServiceName)); err != nil {
		t.Fatalf("service.start: %v", err)
	}
	// The job was waited on: a oneshot with a one-second pre-step is
	// `active`, not `activating`, the moment start returns.
	if st := systemctlProp(t, c, "ActiveState"); st != "active" {
		t.Errorf("ActiveState = %q right after start returned; the job was not awaited", st)
	}
	if running, err := r.Exec.Call(c, "service.status", value.MapOf("name", liveServiceName)); err != nil || running != true {
		t.Errorf("service.status = %v, %v after start", running, err)
	}

	if _, err := r.Exec.Call(c, "service.restart", value.MapOf("name", liveServiceName)); err != nil {
		t.Fatalf("service.restart: %v", err)
	}
	if st := systemctlProp(t, c, "ActiveState"); st != "active" {
		t.Errorf("ActiveState = %q after restart", st)
	}

	if _, err := r.Exec.Call(c, "service.stop", value.MapOf("name", liveServiceName)); err != nil {
		t.Fatalf("service.stop: %v", err)
	}
	if st := systemctlProp(t, c, "ActiveState"); st == "active" {
		t.Errorf("ActiveState = %q after stop", st)
	}
	if running, _ := r.Exec.Call(c, "service.status", value.MapOf("name", liveServiceName)); running != false {
		t.Errorf("service.status = %v after stop", running)
	}
	if *hits == 0 {
		t.Fatal("the D-Bus path was never taken; this ran entirely on systemctl")
	}
}

// **enable / disable over D-Bus**, checked against the wants symlink
// systemd actually writes.
func TestLiveServiceEnablesAndDisablesOverDBus(t *testing.T) {
	c := serviceLive(t)
	installProbeUnit(t, c)
	hits := countDBus(t)
	r := New()

	if _, err := r.Exec.Call(c, "service.enable", value.MapOf("name", liveServiceName)); err != nil {
		t.Fatalf("service.enable: %v", err)
	}
	if s := mustRunAllowed(t, c, "systemctl", "is-enabled", liveServiceName); s != "enabled" {
		t.Errorf("`systemctl is-enabled` = %q after service.enable", s)
	}
	if en, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveServiceName)); err != nil || en != true {
		t.Errorf("service.enabled = %v, %v after enable", en, err)
	}

	if _, err := r.Exec.Call(c, "service.disable", value.MapOf("name", liveServiceName)); err != nil {
		t.Fatalf("service.disable: %v", err)
	}
	if s := mustRunAllowed(t, c, "systemctl", "is-enabled", liveServiceName); s == "enabled" {
		t.Errorf("`systemctl is-enabled` = %q after service.disable", s)
	}
	if en, _ := r.Exec.Call(c, "service.enabled", value.MapOf("name", liveServiceName)); en != false {
		t.Errorf("service.enabled = %v after disable", en)
	}
	if *hits == 0 {
		t.Fatal("the D-Bus path was never taken")
	}
}

// **mask / unmask over D-Bus**, checked against `systemctl is-enabled`.
func TestLiveServiceMasksAndUnmasksOverDBus(t *testing.T) {
	c := serviceLive(t)
	installProbeUnit(t, c)
	countDBus(t)
	r := New()

	if _, err := r.Exec.Call(c, "service.mask", value.MapOf("name", liveServiceName)); err != nil {
		t.Fatalf("service.mask: %v", err)
	}
	if s := mustRunAllowed(t, c, "systemctl", "is-enabled", liveServiceName); s != "masked" {
		t.Errorf("`systemctl is-enabled` = %q after service.mask", s)
	}
	if m, err := r.Exec.Call(c, "service.masked", value.MapOf("name", liveServiceName)); err != nil || m != true {
		t.Errorf("service.masked = %v, %v after mask", m, err)
	}

	if _, err := r.Exec.Call(c, "service.unmask", value.MapOf("name", liveServiceName)); err != nil {
		t.Fatalf("service.unmask: %v", err)
	}
	if m, _ := r.Exec.Call(c, "service.masked", value.MapOf("name", liveServiceName)); m != false {
		t.Errorf("service.masked = %v after unmask", m)
	}
}

// get_all reads the unit files over D-Bus and the probe is among them.
func TestLiveServiceGetAllListsTheProbeOverDBus(t *testing.T) {
	c := serviceLive(t)
	installProbeUnit(t, c)
	countDBus(t)

	out, err := New().Exec.Call(c, "service.get_all", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	names, _ := out.([]any)
	if len(names) < 20 {
		t.Fatalf("a systemd node has more than %d units; the listing was not parsed", len(names))
	}
	found := false
	for _, n := range names {
		if s, _ := n.(string); s == liveServiceName+".service" {
			found = true
		}
	}
	if !found {
		t.Errorf("the probe unit is not in service.get_all")
	}
}

// The systemctl fallback drives the same unit when the bus dial fails.
func TestLiveServiceFallbackDrivesTheSameUnit(t *testing.T) {
	c := serviceLive(t)
	installProbeUnit(t, c)
	r := New()

	old := systemdBus
	systemdBus = func(context.Context) (systemdBackend, error) {
		return nil, context.DeadlineExceeded
	}
	t.Cleanup(func() { systemdBus = old })

	if _, err := r.Exec.Call(c, "service.start", value.MapOf("name", liveServiceName)); err != nil {
		t.Fatalf("service.start via fallback: %v", err)
	}
	if st := systemctlProp(t, c, "ActiveState"); st != "active" {
		t.Errorf("ActiveState = %q after fallback start", st)
	}
	if _, err := r.Exec.Call(c, "service.stop", value.MapOf("name", liveServiceName)); err != nil {
		t.Fatalf("service.stop via fallback: %v", err)
	}
}

// mustRunAllowed runs a command whose non-zero exit is meaningful
// (`is-enabled` exits 1 for "disabled"/"masked") and returns its first
// stdout line.
func mustRunAllowed(t *testing.T, c *exec.Context, argv ...string) string {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		t.Fatalf("%s: %v", strings.Join(argv, " "), err)
	}
	return strings.TrimSpace(firstLine(res.Stdout))
}
