package main

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The noop reaction, at a rate a laptop does not notice: every event
// offered is accepted, and the hub's own counters say every one was
// reacted to, none dropped, and the queue emptied.
func TestEventsReachTheReactorAndAreCounted(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and the harness")
	}
	sf, root, addr := stormHub(t, "manual", "-n", "4")
	r := runEvents(t, sf, root, addr, "-n", "4", "-rate", "40", "-duration", "2s", "-reaction", "noop")
	if r.Offered != 80 || r.NotOffered != 0 || r.Accepted != 80 || r.Failed != 0 || r.RateLimited != 0 {
		t.Fatalf("offered %d (not offered %d), accepted %d, failed %d, rate limited %d; want 80 offered and accepted, nothing else\n%v",
			r.Offered, r.NotOffered, r.Accepted, r.Failed, r.RateLimited, r.Errors)
	}
	if r.Reacted != 80 || r.ReactorDropped != 0 || r.ReactorFailures != 0 || !r.Drained {
		t.Fatalf("the hub reacted to %.0f, dropped %.0f, failed %.0f, drained %v; want 80 reacted and nothing else",
			r.Reacted, r.ReactorDropped, r.ReactorFailures, r.Drained)
	}
	if r.Published < 80 {
		t.Fatalf("the hub published %.0f events; want at least the 80 accepted", r.Published)
	}
}

// One worker and a queue of two, offered more than that: whatever the
// timing, every accepted event is accounted for, reacted to or counted
// as dropped, and the queue never holds more than its bound. That is
// SPEC 30's "bounded queue behaviour", and the accounting is what lets a
// run beyond 5,000 a second say so.
func TestEventsOverABoundedQueueAreAllAccountedFor(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and the harness")
	}
	sf, root, addr := stormHub(t, "manual", "-n", "4", "-reactor-workers", "1", "-reactor-queue-depth", "2")
	r := runEvents(t, sf, root, addr, "-n", "4", "-rate", "200", "-duration", "1s", "-reaction", "noop")
	if r.Accepted == 0 || r.Failed != 0 {
		t.Fatalf("accepted %d, failed %d: %v", r.Accepted, r.Failed, r.Errors)
	}
	if !r.Drained || r.Reacted+r.ReactorDropped != float64(r.Accepted) {
		t.Fatalf("accepted %d, reacted %.0f, dropped %.0f, drained %v: an accepted event is unaccounted for",
			r.Accepted, r.Reacted, r.ReactorDropped, r.Drained)
	}
	if r.MaxQueueDepth > 2 {
		t.Fatalf("the queue held %.0f; its bound is 2", r.MaxQueueDepth)
	}
	t.Logf("accepted %d: reacted %.0f, dropped %.0f", r.Accepted, r.Reacted, r.ReactorDropped)
}

// The ping reaction dispatches a job per event, as the reactor's own
// principal, to nodes a driver holds connected: every event is reacted
// to and none fails to dispatch, which also shows setup's policy
// binding for that principal is what the hub reads.
func TestEventsWithThePingReactionDispatchAJobEach(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and the harness")
	}
	sf, root, addr := stormHub(t, "manual", "-n", "4")
	driver := exec.Command(sf, "run", "-root", root, "-hub", addr, "-server-name", "127.0.0.1",
		"-n", "4", "-no-submit", "-out", filepath.Join(t.TempDir(), "driver.json"), "-hold", "60s")
	if err := driver.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Process.Kill(); _ = driver.Wait() })
	time.Sleep(3 * time.Second) // four nodes connect well inside this
	r := runEvents(t, sf, root, addr, "-n", "4", "-rate", "20", "-duration", "1s", "-reaction", "ping")
	if r.Accepted != 20 || r.Reacted != 20 || r.ReactorFailures != 0 || r.ReactorDropped != 0 || !r.Drained {
		t.Fatalf("accepted %d, reacted %.0f, failed %.0f, dropped %.0f, drained %v; want 20 reacted and dispatched\n%v",
			r.Accepted, r.Reacted, r.ReactorFailures, r.ReactorDropped, r.Drained, r.Errors)
	}
}

func runEvents(t *testing.T, sf, root, addr string, args ...string) eventsResult {
	t.Helper()
	jsonl := filepath.Join(t.TempDir(), "events.jsonl")
	all := append([]string{"events", "-root", root, "-hub", addr, "-server-name", "127.0.0.1",
		"-concurrency", "8", "-drain-timeout", "20s", "-json", jsonl}, args...)
	out, err := exec.Command(sf, all...).CombinedOutput()
	if err != nil {
		t.Fatalf("events: %v\n%s", err, out)
	}
	t.Logf("events printed:\n%s", out)
	var r eventsResult
	if err := readJSON(jsonl, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
