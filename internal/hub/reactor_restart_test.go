package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
)

// A reactor stopped with an event still debouncing reacts to it after a restart.
//
// SPEC 17.2 says replay from an offset "makes a reactor restart lossless".
// The reactor wrote its position as soon as it had *offered* a batch to its
// workers, so an event waiting out a debounce window, queued behind a busy
// worker, or in the middle of its reaction when the hub stopped was already
// behind the recorded position: the next start resumed after it, and the
// reaction never happened and left no trace. The existing restart test covers
// only events that arrived while no reactor was running. DIVERGENCE 5.495.
func TestAnEventStillDebouncingWhenTheReactorStopsIsReactedToAfterARestart(t *testing.T) {
	l, first, _ := reactorLab(t, map[string]string{
		"noop.sls": "ping:\n  local.test.ping:\n    - tgt: '*'\n",
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls:
      - $DIR/noop.sls
    principal: 'cert:CN=ed'
    debounce: 2s
`)
	l.server.Policy = labPolicy(t)
	l.enrolled(t, "web1.example")
	offsetFile := filepath.Join(t.TempDir(), "reactor.offset")
	first.OffsetFile = offsetFile
	first.writeOffset(eventbus.Earliest)

	l.server.emit("halite/beacon/web1.example/inotify/etc/thing", "web1.example",
		map[string]any{"path": "/etc/thing"})

	// The first reactor reads the event and starts its debounce, then stops
	// well inside the two seconds.
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = first.Run(ctx)
	}()
	deadline := time.After(5 * time.Second)
	for first.readOffset() == eventbus.Earliest {
		select {
		case <-deadline:
			t.Fatal("the first reactor never read the event")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-stopped

	// A new reactor, the same bus, the same position file.
	second := &Reactor{
		Server: l.server, Entries: l.server.Reactors, Workers: 2, Now: l.server.now,
		OffsetFile: offsetFile,
	}
	reacted := make(chan []ReactionResult, 4)
	second.Handled = func(_ string, res []ReactionResult) { reacted <- res }
	ctx2, cancel2 := context.WithCancel(context.Background())
	stopped2 := make(chan struct{})
	go func() {
		defer close(stopped2)
		_ = second.Run(ctx2)
	}()
	t.Cleanup(func() { cancel2(); <-stopped2 })

	select {
	case <-reacted:
	case <-time.After(6 * time.Second):
		t.Fatal("the event that was debouncing when the reactor stopped was lost: " +
			"the restarted reactor resumed after it")
	}
}

// Events the reactor finished are not read again after a restart.
//
// The other half of the test above: a position held back by every event
// would be a reactor that replays its whole backlog on each start. Three
// events are handled, the position is waited for until it is the end of the
// bus, and a second reactor started from the file sees nothing.
func TestEventsAlreadyReactedToAreNotRepeatedAfterARestart(t *testing.T) {
	l, first, _ := reactorLab(t, map[string]string{
		"noop.sls": "ping:\n  local.test.ping:\n    - tgt: '*'\n",
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls:
      - $DIR/noop.sls
    principal: 'cert:CN=ed'
`)
	l.server.Policy = labPolicy(t)
	l.enrolled(t, "web1.example")
	offsetFile := filepath.Join(t.TempDir(), "reactor.offset")
	first.OffsetFile = offsetFile
	first.writeOffset(eventbus.Earliest)
	for i := 0; i < 3; i++ {
		l.server.emit("halite/beacon/web1.example/inotify/etc/thing", "web1.example",
			map[string]any{"path": "/etc/thing", "n": int64(i)})
	}
	handled := make(chan struct{}, 8)
	first.Handled = func(string, []ReactionResult) { handled <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = first.Run(ctx)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-handled:
		case <-time.After(5 * time.Second):
			t.Fatalf("the first reactor handled %d of 3 events", i)
		}
	}
	// The position it records is the end of the bus once every event is done.
	_, end, err := l.server.Events.Read(eventbus.Earliest, nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for first.readOffset() != end {
		select {
		case <-deadline:
			t.Fatalf("the recorded position is %q, want the end of the bus %q: "+
				"finished events are still holding it back", first.readOffset(), end)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-stopped

	second := &Reactor{
		Server: l.server, Entries: l.server.Reactors, Workers: 2, Now: l.server.now,
		OffsetFile: offsetFile,
	}
	repeated := make(chan struct{}, 8)
	second.Handled = func(string, []ReactionResult) { repeated <- struct{}{} }
	ctx2, cancel2 := context.WithCancel(context.Background())
	stopped2 := make(chan struct{})
	go func() {
		defer close(stopped2)
		_ = second.Run(ctx2)
	}()
	t.Cleanup(func() { cancel2(); <-stopped2 })
	select {
	case <-repeated:
		t.Error("an event that had been reacted to was reacted to again after the restart")
	case <-time.After(700 * time.Millisecond):
	}
}

// The tracker holds the position at the oldest unfinished event, and lets it
// move as they finish, in whatever order they do.
func TestTheTrackerHoldsThePositionAtTheOldestUnfinishedEvent(t *testing.T) {
	var tr ackTracker
	a, b, c := &eventbus.Event{}, &eventbus.Event{}, &eventbus.Event{}
	// Each event is offered to one entry (a hold), then the offer's own hold
	// is let go, as Run and offerTo do.
	for _, step := range []struct {
		e      *eventbus.Event
		before string
	}{{a, "p0"}, {b, "p1"}, {c, "p2"}} {
		tr.track(step.e, step.before)
		tr.hold(step.e)
		tr.release(step.e)
	}
	if got := tr.committed("end"); got != "p0" {
		t.Errorf("with three events held the position is %q, want p0", got)
	}
	// A is a reaction in progress; B and C finish first.
	tr.release(c)
	tr.release(b)
	if got := tr.committed("end"); got != "p0" {
		t.Errorf("with the oldest unfinished the position is %q, want p0", got)
	}
	tr.release(a)
	if got := tr.committed("end"); got != "end" {
		t.Errorf("with everything finished the position is %q, want the end", got)
	}
	// An event nobody tracked is ignored rather than crashing.
	tr.hold(&eventbus.Event{})
	tr.release(&eventbus.Event{})
}

// Events that are replaced while debouncing, and ones the queue drops, stop
// holding the position: both are reported or coalesced, not waiting.
func TestDebouncedAndDroppedEventsStopHoldingThePosition(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"noop.sls": "ping:\n  local.test.ping:\n    - tgt: '*'\n",
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls:
      - $DIR/noop.sls
    principal: 'cert:CN=ed'
    debounce: 1h
`)
	_ = l
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)

	first := &eventbus.Event{Tag: "halite/beacon/w/x", Node: "w", Data: map[string]any{}}
	second := &eventbus.Event{Tag: "halite/beacon/w/x", Node: "w", Data: map[string]any{}}
	r.acks.track(first, "p0")
	r.Offer(first)
	r.acks.release(first)
	r.acks.track(second, "p1")
	r.Offer(second)
	r.acks.release(second)
	// The first was replaced by the second, which is still debouncing.
	if got := r.acks.committed("end"); got != "p1" {
		t.Errorf("the position is %q after the first event was replaced, want p1", got)
	}
}

// An event the full queue drops stops holding the position: the loss is
// reported on the bus and in a metric, and keeping the file behind it would
// replay the whole backlog at every start for as long as the queue overflowed.
func TestAnEventTheQueueDropsStopsHoldingThePosition(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"noop.sls": "ping:\n  local.test.ping:\n    - tgt: '*'\n",
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls:
      - $DIR/noop.sls
    principal: 'cert:CN=ed'
`)
	_ = l
	// One worker with room for one job and nothing popping from it, so the
	// second and third offers each push the previous job out.
	r.limits = map[string]*reactorLimit{}
	r.chains = map[string]*chainCount{}
	r.workers = []*reactorWorker{{reactor: r, queue: newBoundedQueue(1)}}

	for i, before := range []string{"p0", "p1", "p2"} {
		e := &eventbus.Event{Tag: "halite/beacon/w/x", Node: "w", Data: map[string]any{"i": int64(i)}}
		r.acks.track(e, before)
		r.Offer(e)
		r.acks.release(e)
	}
	if got := r.workers[0].queue.Len(); got != 1 {
		t.Fatalf("the queue holds %d jobs, want 1", got)
	}
	if got := r.acks.committed("end"); got != "p2" {
		t.Errorf("the position is %q, want p2: the dropped events still hold it back", got)
	}
}
