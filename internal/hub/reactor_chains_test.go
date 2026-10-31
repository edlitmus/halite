package hub

import (
	"fmt"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
)

// Counting a causality chain does not walk every chain held, per event.
//
// overLength swept the whole map for forgotten chains on each call, under the
// lock each event takes, and an event with no chain starts one, so the map held
// ten minutes of events and each new event walked all of them. Measured on
// this workstation, counting alone: 74 us per event at 10,000 chains held,
// 335 us at 40,000, 813 us at 80,000. SPEC 30's 5,000 events a second leaves
// 200 us, and that was gone within seconds (DIVERGENCE 5.494).
//
// The count of sweeps is asserted rather than the time taken, which would
// depend on the machine: with the old code every one of the 50,000 events
// below swept, and with the interval one does.
func TestCountingChainsDoesNotSweepOnEveryEvent(t *testing.T) {
	clock := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	r := &Reactor{chains: map[string]*chainCount{}, Now: func() time.Time { return clock }}

	for i := 0; i < 50000; i++ {
		r.overLength(&eventbus.Event{Correlation: fmt.Sprintf("c%d", i)})
	}
	if r.chainScans != 1 {
		t.Errorf("50,000 events inside one interval swept the chain map %d times, want 1", r.chainScans)
	}
	if len(r.chains) != 50000 {
		t.Fatalf("held %d chains, want 50000", len(r.chains))
	}

	// Forgetting still happens: past the age and the interval, the next event
	// sweeps away what was not seen again.
	clock = clock.Add(chainForgetAfter + chainPruneEvery + time.Second)
	r.overLength(&eventbus.Event{Correlation: "fresh"})
	if r.chainScans != 2 {
		t.Errorf("a sweep was due and %d have run", r.chainScans)
	}
	if len(r.chains) != 1 {
		t.Errorf("%d chains held after the old ones aged out, want only the fresh one", len(r.chains))
	}
}

// A chain seen again is not forgotten, however long the sweeps are apart.
func TestAChainSeenAgainIsNotForgotten(t *testing.T) {
	clock := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	r := &Reactor{chains: map[string]*chainCount{}, MaxDepth: 5, Now: func() time.Time { return clock }}
	for minute := 0; minute < 30; minute++ {
		r.overLength(&eventbus.Event{Correlation: "loop"})
		clock = clock.Add(time.Minute)
	}
	if got := r.chains["loop"]; got == nil || got.n != 30 {
		t.Errorf("the chain seen every minute holds %+v, want a count of 30", got)
	}
	if !r.overLength(&eventbus.Event{Correlation: "loop"}) {
		t.Error("a chain past its depth was not broken")
	}
}
