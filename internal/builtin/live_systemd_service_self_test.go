package builtin

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The self-restart guard of issue 728 on the **systemd** side, driven
// inside a real transient unit.
//
// # Why it has to re-exec itself
//
// The FreeBSD half of this guard can be tested by writing an rc.d script
// that names the test process (see `live_freebsd_service_self_test.go`):
// rc.subr is told where to look. systemd is not. A unit's processes are
// the ones in its cgroup, and the only way to put this test in a unit's
// cgroup is to *be* in one. So the outer run starts the same test binary
// again under a transient unit with `systemd-run --pipe --wait`, and the
// inner run -- which is the one in the unit -- does the asserting. The
// unit's name is passed in the environment, which is also the flag that
// tells the inner run which half it is.
//
// `--pipe` is what keeps a failure readable: without it the inner run's
// output goes to the journal and the outer test can report only an exit
// status. `--collect` takes the unit away afterwards even when it failed.
//
// # What it proves that the unit tests cannot
//
// That `/proc/self/cgroup` really holds what this project captured, on a
// live systemd; that a *transient* unit reads as the unit it is; and that
// the refusal fires there. The last one is self-demonstrating in a way
// the FreeBSD test is not: with the guard removed, `service.restart` on
// the inner run's own unit restarts the unit, which kills the inner run,
// and the outer test reports the signal.
//
// # Running it
//
//	sudo HALITE_SYSTEM_LIVE=1 go test -run TestLiveSystemdSelfRestart -v ./internal/builtin/
const liveSystemdSelfUnitPrefix = "haliteprobeself"

// liveSystemdSelfInnerEnv carries the unit name to the inner run, and
// its presence is what marks that run as the inner one.
const liveSystemdSelfInnerEnv = "HALITE_LIVE_SYSTEMD_SELF_UNIT"

func TestLiveSystemdSelfRestartIsRefused(t *testing.T) {
	c := serviceLive(t)
	if c == nil {
		// serviceLive has already skipped: systemctl is absent on a
		// family that is allowed not to have it.
		return
	}
	if unit := os.Getenv(liveSystemdSelfInnerEnv); unit != "" {
		liveSystemdSelfAssertions(t, c, unit)
		return
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("this process cannot name its own binary: %v", err)
	}
	// The pid is in the unit name so that two runs on one machine, or a
	// run after one that was killed before `--collect` fired, do not
	// collide on a name systemd still holds.
	unit := fmt.Sprintf("%s-%d", liveSystemdSelfUnitPrefix, os.Getpid())
	res, err := c.Run(exec.Command{
		Argv: []string{
			"systemd-run", "--collect", "--wait", "--pipe", "--quiet",
			"--unit=" + unit,
			"--setenv=" + liveSystemdSelfInnerEnv + "=" + unit,
			"--setenv=HALITE_SYSTEM_LIVE=1",
			self, "-test.run=^" + t.Name() + "$", "-test.v=true",
		},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("`systemd-run` could not start the inner run: %v", err)
	}
	t.Logf("the inner run, under %s.service:\n%s%s", unit, res.Stdout, res.Stderr)
	if res.Code != 0 {
		t.Fatalf("the inner run under %s.service exited %d", unit, res.Code)
	}
}

// liveSystemdSelfAssertions is the half that runs inside the unit.
func liveSystemdSelfAssertions(t *testing.T, c *exec.Context, unit string) {
	// The real file first, logged, because it is the thing the unit
	// tests next door hold a capture of. If its shape ever changes, it
	// changes here.
	raw, err := os.ReadFile(selfCgroupPath)
	if err != nil {
		t.Fatalf("reading %s: %v", selfCgroupPath, err)
	}
	t.Logf("%s inside %s.service: %q", selfCgroupPath, unit, string(raw))

	units, err := systemdSelfUnits()
	if err != nil {
		t.Fatalf("systemdSelfUnits: %v", err)
	}
	t.Logf("the units on this process's cgroup path: %q", units)
	want := unit + ".service"
	found := false
	for _, u := range units {
		if u == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("this process's cgroup does not name %s: %q", want, units)
	}

	isSelf, err := (systemdProvider{}).RunsThisProcess(c, unit)
	if err != nil {
		t.Fatalf("RunsThisProcess(%q): %v", unit, err)
	}
	if !isSelf {
		t.Fatalf("the unit this process runs under was not recognised as its own: %q", unit)
	}

	// The control. A unit that exists on the machine but is not on this
	// process's cgroup path must read as somebody else's -- `halite-node`
	// is the one issue 728 was about, and on a host running it this is
	// the assertion that the guard is about *this* process rather than
	// about the name.
	for _, other := range []string{"halite-node", liveSystemdSelfUnitPrefix + "-not-this-one"} {
		isOther, err := (systemdProvider{}).RunsThisProcess(c, other)
		if err != nil {
			t.Fatalf("RunsThisProcess(%q): %v", other, err)
		}
		if isOther {
			t.Errorf("the unit %q was taken for the one running this process", other)
		}
	}

	refusal := serviceSelfActionRefusal(systemdProvider{}, c, unit, "restarting")
	for _, phrase := range []string{
		"is the one running this process",
		systemdProvider{}.SelfActionConsequence(),
		"systemd-run --on-active=30 --unit=" + unit + "-restart systemctl restart " + unit + ".service",
	} {
		if !strings.Contains(refusal, phrase) {
			t.Errorf("the refusal does not name %q:\n%s", phrase, refusal)
		}
	}

	// And through the registry, which is the path a job takes. Without
	// the guard this call restarts the unit and kills this process, so
	// reaching the next line at all is part of the result.
	if _, err := New().Exec.Call(c, "service.restart", value.MapOf("name", unit)); err == nil {
		t.Fatal("service.restart on this process's own unit returned no error")
	} else if !strings.Contains(err.Error(), "is the one running this process") {
		t.Fatalf("service.restart failed for the wrong reason: %v", err)
	}
	t.Log("service.restart was refused and this process is still alive")
}
