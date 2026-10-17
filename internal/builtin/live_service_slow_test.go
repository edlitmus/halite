package builtin

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

const liveSlowServiceName = "halite-live-slow"

// Longer than systemd's own default start timeout, 90 s, and allowed to be
// by the unit, as a database's or a migration's is.
const liveSlowServiceUnit = `[Unit]
Description=halite live-test probe that takes 95 s to start

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sleep 95
TimeoutStartSec=180
`

// **A start that takes longer than 90 s is waited for** (issue 321,
// M-48). The D-Bus provider set a read deadline of 90 s from the dial
// whenever the context had none, so a unit whose TimeoutStartSec allows
// more was reported failed at 90 s while systemd went on starting it.
// `systemctl start`, the fallback and what Salt runs, waits for the job.
// Slow on purpose: this takes a minute and a half.
func TestLiveServiceWaitsForAStartLongerThanNinetySeconds(t *testing.T) {
	c := serviceLive(t)
	installSlowUnit(t, c)
	hits := countDBus(t)

	start := time.Now()
	_, err := New().Exec.Call(c, "service.start", value.MapOf("name", liveSlowServiceName))
	took := time.Since(start)
	if err != nil {
		t.Fatalf("service.start returned after %v: %v", took.Round(time.Second), err)
	}
	if took < 95*time.Second {
		t.Errorf("service.start returned after %v, before the unit could have started", took.Round(time.Second))
	}
	res, _ := c.Run(exec.Command{Argv: []string{"systemctl", "is-active", liveSlowServiceName}, IgnoreExitCode: true})
	if st := strings.TrimSpace(res.Stdout); st != "active" {
		t.Errorf("the unit is %q after service.start returned", st)
	}
	if *hits == 0 {
		t.Fatal("the D-Bus path was never taken; this ran entirely on systemctl")
	}
}

// A deadline the caller set still ends the wait: a state's `timeout`, or a
// job's, is the caller saying how long is too long.
func TestLiveServiceStartStopsAtTheCallersDeadline(t *testing.T) {
	c := serviceLive(t)
	installSlowUnit(t, c)
	countDBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cc := *c
	cc.Ctx = ctx

	start := time.Now()
	_, err := New().Exec.Call(&cc, "service.start", value.MapOf("name", liveSlowServiceName))
	took := time.Since(start)
	if err == nil {
		t.Fatalf("service.start of a 95 s unit under a 5 s deadline succeeded after %v", took.Round(time.Second))
	}
	if took > 15*time.Second {
		t.Errorf("service.start under a 5 s deadline returned after %v: %v", took.Round(time.Second), err)
	}
}

func installSlowUnit(t *testing.T, c *exec.Context) {
	t.Helper()
	path := "/run/systemd/system/" + liveSlowServiceName + ".service"
	if err := os.WriteFile(path, []byte(liveSlowServiceUnit), 0o644); err != nil {
		t.Fatalf("writing the probe unit: %v", err)
	}
	mustRun(t, c, "systemctl", "daemon-reload")
	t.Cleanup(func() {
		for _, argv := range [][]string{
			{"systemctl", "stop", liveSlowServiceName},
			{"systemctl", "reset-failed", liveSlowServiceName},
		} {
			_, _ = c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		}
		_ = os.Remove(path)
		_, _ = c.Run(exec.Command{Argv: []string{"systemctl", "daemon-reload"}, IgnoreExitCode: true})
	})
}
