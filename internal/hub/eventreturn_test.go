package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
)

// recordingReturner stands in for a SIEM, and can be made to fail.
type recordingReturner struct {
	mu     sync.Mutex
	got    []string
	failAt int
	fails  bool
}

func (r *recordingReturner) Name() string { return "recording" }

func (r *recordingReturner) Return(ctx context.Context, ret *job.Return) error { return nil }

func (r *recordingReturner) Event(ctx context.Context, e *eventbus.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fails && len(r.got) >= r.failAt {
		return errors.New("the receiver is down")
	}
	r.got = append(r.got, e.Tag)
	return nil
}

func (r *recordingReturner) Close() error { return nil }

func (r *recordingReturner) tags() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func eventReturnLab(t *testing.T, sink *recordingReturner) (*EventReturn, *eventbus.Bus, string) {
	t.Helper()
	dir := t.TempDir()
	bus, err := eventbus.Open(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bus.Close() })
	srv := &Server{Events: bus}
	offset := filepath.Join(dir, "event-return.offset")
	return &EventReturn{
		Server: srv, Returner: sink, OffsetFile: offset, Batch: 10,
		From: eventbus.Earliest,
	}, bus, offset
}

func TestTheEventStreamReachesTheReturner(t *testing.T) {
	sink := &recordingReturner{}
	shipper, bus, _ := eventReturnLab(t, sink)

	for _, tag := range []string{"halite/job/1/new", "halite/job/1/ret/web1", "halite/node/web1/start"} {
		if _, err := bus.Append(&eventbus.Event{Tag: tag, Stamp: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	// From the beginning, since the events are already on the bus.
	shipper.OffsetFile = ""
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		shipper2 := *shipper
		_ = shipper2.Run(ctx)
	}()

	waitForTags(t, sink, 3)
	if got := len(sink.tags()); got != 3 {
		t.Errorf("shipped %d events, want 3", got)
	}
}

// A returner outage must not become permanent loss — the offset does
// not advance past an event that did not arrive.
func TestAFailedShipmentDoesNotAdvanceTheOffset(t *testing.T) {
	sink := &recordingReturner{fails: true, failAt: 2}
	shipper, bus, offsetFile := eventReturnLab(t, sink)
	shipper.OffsetFile = offsetFile

	for i := 0; i < 4; i++ {
		if _, err := bus.Append(&eventbus.Event{Tag: "halite/job/x/new", Stamp: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = shipper.Run(ctx)

	// Two got through and the rest did not, so nothing was recorded as
	// shipped: the next start sends all four again rather than skipping
	// the two that never arrived.
	if _, err := os.Stat(offsetFile); err == nil {
		t.Errorf("the offset advanced past events that were never delivered")
	}
	if got := len(sink.tags()); got != 2 {
		t.Errorf("the sink took %d events, want 2", got)
	}
}

func waitForTags(t *testing.T, sink *recordingReturner, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(sink.tags()) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("only %d events arrived, want %d", len(sink.tags()), want)
}
