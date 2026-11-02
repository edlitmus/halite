package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/value"
)

func TestThePillarCacheCompilesOncePerWindowAndRetriesAFailureSooner(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	calls, fail := 0, false
	var warned int
	c := newPillarCache(func() (*value.Map, error) {
		calls++
		if fail {
			return nil, errors.New("the pillar does not compile")
		}
		return value.MapOf("n", int64(calls)), nil
	}, func(string, ...any) { warned++ })
	c.now = func() time.Time { return now }

	for i := 0; i < 50; i++ {
		c.get()
	}
	if calls != 1 {
		t.Fatalf("50 polls compiled %d times, want 1", calls)
	}
	now = now.Add(beaconPillarTTL - time.Second)
	c.get()
	if calls != 1 {
		t.Errorf("just inside the window it compiled again (%d)", calls)
	}
	now = now.Add(2 * time.Second)
	if v, _ := c.get().Get("n"); v != int64(2) || calls != 2 {
		t.Errorf("past the window: n=%v after %d compilations, want a fresh one", v, calls)
	}

	// A failure is an empty pillar, said once per attempt, and retried
	// after a minute and not at the rate of the poll.
	fail = true
	now = now.Add(beaconPillarTTL + time.Second)
	if got := c.get(); got.Len() != 0 {
		t.Errorf("a pillar that did not compile was not empty: %v", got)
	}
	for i := 0; i < 20; i++ {
		c.get()
	}
	if calls != 3 || warned != 1 {
		t.Errorf("20 polls after a failure: %d compilations (want 3 in all) and %d warnings (want 1)", calls, warned)
	}
	fail = false
	now = now.Add(beaconPillarFailedTTL + time.Second)
	if v, _ := c.get().Get("n"); v != int64(4) {
		t.Errorf("after the failure window the pillar was not compiled again: %v", v)
	}
}

type nullEvents struct{}

func (nullEvents) Send(string, map[string]any) error { return nil }

// Through the real path: a beacon polled every second asks the hub for the
// pillar once, not once a poll. DIVERGENCE 5.552.
func TestABeaconPolledEverySecondDoesNotAskTheHubForPillarEveryTime(t *testing.T) {
	n := nodeWithBrokenPillar(t)
	root := t.TempDir()
	// diskusage, which runs on every platform (the load beacon does not run
	// on Windows, and a node told to start one there exits). The threshold is
	// one the disk cannot reach, so the beacon polls and never fires; what is
	// counted is the context each poll asks for.
	cfg, err := config.Load(config.Node, config.LoadOptions{Path: writeConfig(t, root,
		"beacon_tick: 100ms\nbeacons:\n  diskusage:\n    - interval: 1\n    - '"+filepath.ToSlash(root)+"': 100%\n")})
	if err != nil {
		t.Fatal(err)
	}
	n.cfg, n.root, n.events = cfg, root, nullEvents{}
	var asked atomic.Int64
	n.hubPillar = func(string, bool) (*value.Map, error) {
		asked.Add(1)
		return value.NewMap(0), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer cancel()
	n.startBeacons(ctx)
	<-ctx.Done()
	if got := asked.Load(); got != 1 {
		t.Errorf("the hub was asked for this node's pillar %d times in 3.5 seconds of a one-second beacon, want 1", got)
	}
}
