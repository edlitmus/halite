package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// launchdPrintCapture is a real `launchctl print` answer, captured
// verbatim from the `macos` leg of `fleet.yml` — a macOS 15 runner, as
// root, printing the LaunchDaemon `live_mac_service_test.go` brings with
// it, after bootstrapping it and before anything had started it.
//
// It carries the two things this parser depends on and no invented
// fixture would have gotten right: `runs` is a bare key in the job's
// top-level block rather than anything nested, and launchd prints
// `minimum runtime = 10` — which is the throttle, in the tool's own
// words. A job asked to start again inside that window is held until it
// passes, and `launchctl start` returns without saying so. DIVERGENCE
// 5.122.
const launchdPrintCapture = `system/org.halite.live-probe = {
	active count = 0
	path = /Library/LaunchDaemons/org.halite.live-probe.plist
	type = LaunchDaemon
	state = not running

	program = /bin/sh
	arguments = {
		/bin/sh
		-c
		while :; do /bin/sleep 1; done
	}

	default environment = {
		PATH => /usr/bin:/bin:/usr/sbin:/sbin
	}

	environment = {
		XPC_SERVICE_NAME => org.halite.live-probe
	}

	domain = system
	minimum runtime = 10
	exit timeout = 5
	runs = 0
	last exit code = (never exited)

	spawn type = daemon (3)
	jetsam priority = 40
	jetsam memory limit (active) = (unlimited)
	jetsam memory limit (inactive) = (unlimited)
	jetsamproperties category = daemon
	jetsam thread limit = 32
	cpumon = default
	probabilistic guard malloc policy = {
		activation rate = 1/1000
		sample rate = 1/0
	}

	properties = inferred program | system service | tle system
}
`

const launchdPrintCommand = "launchctl print system/org.halite.live-probe"

const launchdStartCommand = "launchctl start org.halite.live-probe"

// The counter comes out of a real print, and the count is the one
// launchd reported.
func TestLaunchdSpawnCountReadsARealPrint(t *testing.T) {
	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{
		Responses: map[string]exec.Result{
			launchdPrintCommand: {Stdout: launchdPrintCapture},
		},
	}

	runs, ok := launchdSpawnCount(c, "org.halite.live-probe")
	if !ok {
		t.Fatal("a real `launchctl print` answer was read as unmeasurable")
	}
	if runs != 0 {
		t.Errorf("runs = %d, and the capture says 0", runs)
	}
}

// A label launchd does not know, or a domain this account cannot print,
// is unmeasurable rather than zero — the difference between "never
// spawned" and "cannot see it", which decides whether the caller waits
// for a respawn that may never be reported.
func TestLaunchdSpawnCountSeparatesUnknownFromNeverRun(t *testing.T) {
	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{
		Responses: map[string]exec.Result{
			"launchctl print system/org.halite.absent": {
				Code:   113,
				Stderr: "Could not find service \"org.halite.absent\" in domain for system\n",
			},
		},
	}

	if _, ok := launchdSpawnCount(c, "org.halite.absent"); ok {
		t.Error("a label launchd does not know was read as a measurable count")
	}
}

// **Start waits until launchd has really spawned the job**, which is the
// defect the macOS leg found: `launchctl start` returns when the request
// is queued, and a throttled respawn arrives up to ten seconds later.
//
// The runner answers the same print differently over time, which is what
// a throttle looks like from outside: the counter sits still and then
// moves. Both answers are the captured output, with the one line that
// differs between two prints of the same job changed.
func TestLaunchdStartWaitsForTheSpawnCounterToMove(t *testing.T) {
	stillThrottled := launchdPrintCapture
	respawned := strings.Replace(launchdPrintCapture, "runs = 0", "runs = 1", 1)
	if respawned == launchdPrintCapture {
		t.Fatal("the capture has no runs line to advance")
	}

	runner := &sequencedRunner{answers: map[string][]exec.Result{
		launchdPrintCommand: {
			{Stdout: stillThrottled}, // the system domain has it, so it is not a user agent
			{Stdout: stillThrottled}, // the baseline, read before the start
			{Stdout: stillThrottled}, // scheduled, not yet spawned
			{Stdout: respawned},
		},
	}}
	c := newCtx(false)
	c.Runner = runner

	if err := (launchdProvider{}).Start(c, "org.halite.live-probe"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ran := runner.commands()
	// The order is the whole point: the baseline has to be read before
	// the start, or there is nothing for the counter to have moved from.
	// (The print ahead of it is launchdUserTarget asking the system
	// domain first, which is the same command.)
	start := -1
	for i, cmd := range ran {
		if cmd == launchdStartCommand {
			start = i
		}
	}
	if start < 1 || ran[start-1] != launchdPrintCommand {
		t.Fatalf("Start ran %v; it must print before it starts", ran)
	}
	if ran[len(ran)-1] != launchdPrintCommand {
		t.Errorf("Start returned without reading the counter again: %v", ran)
	}
}

// And with no baseline it does not wait at all. `launchctl print` needs
// root and the system domain; a node that cannot read the counter must
// behave as it did before the wait existed rather than block for the
// deadline.
func TestLaunchdStartDoesNotWaitWithoutABaseline(t *testing.T) {
	runner := &sequencedRunner{answers: map[string][]exec.Result{
		launchdPrintCommand: {{Code: 113, Stderr: "Bad request.\n"}},
	}}
	c := newCtx(false)
	c.Runner = runner

	if err := (launchdProvider{}).Start(c, "org.halite.live-probe"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Nothing after the start: with no counter to compare against there
	// is nothing to wait for. What comes before it is the domain lookup
	// (the system print, then the console user, who is nobody here) and
	// the one baseline read.
	ran := runner.commands()
	if len(ran) == 0 || ran[len(ran)-1] != launchdStartCommand {
		t.Fatalf("Start ran %v; with no baseline it starts and does not wait", ran)
	}
	starts := 0
	for _, cmd := range ran {
		if cmd == launchdStartCommand {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("Start ran %v; it should start once", ran)
	}
}

// sequencedRunner answers a command differently each time it is asked,
// which is what a counter that moves needs. `changingRunner` in
// dpkg_test.go is the same idea; this one records nothing else and keeps
// the last answer standing, so a poll that comes round once more does
// not fall off the end.
type sequencedRunner struct {
	answers map[string][]exec.Result
	ran     []exec.Command
}

func (r *sequencedRunner) Run(_ context.Context, cmd exec.Command) (exec.Result, error) {
	r.ran = append(r.ran, cmd)
	queued := r.answers[cmd.String()]
	if len(queued) == 0 {
		return exec.Result{}, nil
	}
	next := queued[0]
	if len(queued) > 1 {
		r.answers[cmd.String()] = queued[1:]
	}
	return next, nil
}

func (r *sequencedRunner) commands() []string {
	out := make([]string, len(r.ran))
	for i, cmd := range r.ran {
		out[i] = cmd.String()
	}
	return out
}
