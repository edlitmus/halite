package beacon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
)

// observations is what a caller keeping metrics would be handed.
type observations struct {
	mu     sync.Mutex
	counts map[string]int
	names  map[string]string
}

func newObservations() *observations {
	return &observations{counts: map[string]int{}, names: map[string]string{}}
}

func (o *observations) record(event, beaconName string, n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.counts[event] += n
	o.names[event] = beaconName
}

func (o *observations) get(event string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counts[event]
}

// The queue is bounded and drops the oldest. SPEC 26.2 asks for a
// counter behind every drop path, and this one reported its loss as a
// log line and an event: neither is a number an alert can watch, and
// `halite_beacon_dropped_total` was the half of SPEC's own pair that
// this build did not have.
//
// Driven through offer rather than through Run, because the engine
// floors its queue at DefaultQueueDepth and filling a thousand slots
// would be a test about patience.
func TestTheQueueReportsHowMuchItDropped(t *testing.T) {
	in := instance("diskusage")
	in.CoalesceWindow = time.Hour
	e, _ := testEngine(t, in, nothingToPoll)
	e.queue = newCoalescingQueue(2)
	seen := newObservations()
	e.Observe = seen.record

	for i := range 5 {
		e.offer(in, Event{Suffix: "var", Data: map[string]any{"percent_used": float64(i)}})
	}

	if got := seen.get("fired"); got != 5 {
		t.Errorf("%d events were reported as fired, want 5", got)
	}
	// Two slots, five events: three had to go, and the count is what
	// the metric needs — counting the overflows rather than the events
	// would report one loss where three happened.
	if got := seen.get("dropped"); got != 3 {
		t.Errorf("%d events were reported as dropped, want 3", got)
	}
	if seen.names["dropped"] != "diskusage" {
		t.Errorf("the drop was reported against %q", seen.names["dropped"])
	}
}

// A rate-limited event is not a dropped one. They are different
// failures — one is the beacon behaving as configured and the other is
// the node losing data — and counting them together would make a
// working rate limit look like an outage.
func TestRateLimitingIsReportedSeparately(t *testing.T) {
	in := instance("service")
	in.Interval = time.Millisecond
	in.CoalesceWindow = time.Millisecond
	in.RateLimit = 0.001

	var polls int
	var mu sync.Mutex
	e, _ := testEngine(t, in, func(*exec.Context, *Instance) ([]Event, error) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		return []Event{{Suffix: "nginx", Data: map[string]any{"n": float64(polls)}}}, nil
	})
	seen := newObservations()
	e.Observe = seen.record

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	deadline := time.After(3 * time.Second)
	for seen.get("rate_limited") == 0 {
		select {
		case <-deadline:
			t.Fatal("the rate limit refused nothing")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if got := seen.get("dropped"); got != 0 {
		t.Errorf("a rate-limited event was counted as %d drops", got)
	}
}

// The depth is what the gauge reads. Before Run has built the queue it
// is zero, which is the honest answer: a queue that does not exist
// holds nothing.
func TestDepthIsZeroBeforeTheEngineRuns(t *testing.T) {
	e, _ := testEngine(t, instance("diskusage"), nothingToPoll)
	if got := e.Depth(); got != 0 {
		t.Errorf("depth on an engine that has not run = %d", got)
	}
}

// nothingToPoll is a beacon that reports nothing, for a test that
// drives the engine by hand.
func nothingToPoll(*exec.Context, *Instance) ([]Event, error) { return nil, nil }
