package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/metrics"
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
//
// It does advance past the ones that did. It used to stay put for the whole
// batch, so a receiver that took two of four and then went down was sent the
// two again on the retry, and this test had been written to expect it.
func TestAFailedShipmentAdvancesOnlyPastWhatArrived(t *testing.T) {
	sink := &recordingReturner{fails: true, failAt: 2}
	shipper, bus, offsetFile := eventReturnLab(t, sink)
	shipper.OffsetFile = offsetFile

	var second string
	for i := 0; i < 4; i++ {
		off, err := bus.Append(&eventbus.Event{
			Tag: fmt.Sprintf("halite/job/%d/new", i), Stamp: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			second = off
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = shipper.Run(ctx)

	if got := len(sink.tags()); got != 2 {
		t.Fatalf("the sink took %d events, want 2", got)
	}
	// Recorded as exactly what arrived: not nothing, and not past the third.
	raw, err := os.ReadFile(offsetFile)
	if err != nil {
		t.Fatalf("the offset was not recorded after two events arrived: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != second {
		t.Errorf("the offset is %q, want the position after the second event, %q", got, second)
	}

	// The receiver comes back, and a restart sends the other two and only
	// those.
	sink.mu.Lock()
	sink.fails = false
	sink.mu.Unlock()
	again := &EventReturn{Server: shipper.Server, Returner: sink, OffsetFile: offsetFile, Batch: 10}
	runInBackground(t, again.Run)
	waitForTags(t, sink, 4)
	time.Sleep(100 * time.Millisecond)
	want := []string{"halite/job/0/new", "halite/job/1/new", "halite/job/2/new", "halite/job/3/new"}
	if got := sink.tags(); !slices.Equal(got, want) {
		t.Errorf("the receiver was sent %v, want each event once: %v", got, want)
	}
}

// A shipper that has fallen off the back of the bus resumes at the oldest
// event the bus still holds, not at the end.
//
// Any read error used to set the position to the end of the bus, so retention
// having removed the shipper's position cost every retained event as well,
// which the shipper had not yet sent and a SIEM wanted.
func TestALaggedEventReturnerResumesAtTheOldestRetainedEvent(t *testing.T) {
	sink := &recordingReturner{}
	shipper, bus, offsetFile := eventReturnLab(t, sink)
	bus.SegmentBytes = 512
	bus.MaxBytes = 2048

	stale, err := bus.Append(&eventbus.Event{
		Tag: "halite/chaos/stale", Data: map[string]any{"pad": strings.Repeat("q", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if _, err := bus.Append(&eventbus.Event{
			Tag:  "halite/chaos/churn",
			Data: map[string]any{"n": i, "pad": strings.Repeat("q", 64)},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := bus.Prune(); err != nil {
			t.Fatal(err)
		}
	}
	var lag *eventbus.LagError
	if err := bus.Lag(stale); !errors.As(err, &lag) {
		t.Fatalf("the offset was not pruned (%v); this test needs a bus that has turned over", err)
	}
	if err := os.WriteFile(offsetFile, []byte(stale+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runInBackground(t, shipper.Run)
	waitForTags(t, sink, 1)
	for _, tag := range sink.tags() {
		if tag != "halite/chaos/churn" {
			t.Errorf("shipped %q, which should have been pruned", tag)
		}
	}
}

// A read error that says nothing about the position leaves it alone.
//
// It used to set the position to the end of the bus, so a segment that could
// not be read for a moment cost everything between the offset and now, and a
// persistent failure was retried with no pause at all.
func TestAnUnreadableBusDoesNotMoveTheEventReturnersOffset(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a file this process cannot open, which root and Windows can")
	}
	sink := &recordingReturner{}
	shipper, bus, offsetFile := eventReturnLab(t, sink)
	for i := 0; i < 3; i++ {
		if _, err := bus.Append(&eventbus.Event{Tag: "halite/job/x/new", Stamp: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	seg, at, err := bus.Resolve(eventbus.Earliest)
	if err != nil {
		t.Fatal(err)
	}
	start := fmt.Sprintf("%08d:%d", seg, at)
	if err := os.WriteFile(offsetFile, []byte(start+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(offsetFile), "events", "*"))
	for _, f := range files {
		if err := os.Chmod(f, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(f, 0o600)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	stopped := make(chan struct{})
	go func() { _ = shipper.Run(ctx); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		// The old loop never looked at the context on this path: it read
		// again at once, for ever, and could not be stopped.
		t.Fatal("the event returner did not stop when its context ended")
	}

	if raw, _ := os.ReadFile(offsetFile); strings.TrimSpace(string(raw)) != start {
		t.Errorf("a read error moved the offset from %s to %q", start, strings.TrimSpace(string(raw)))
	}
	if len(sink.tags()) != 0 {
		t.Errorf("shipped %v from a bus that could not be read", sink.tags())
	}

	// Readable again, a fresh shipper delivers all three: nothing was skipped.
	for _, f := range files {
		os.Chmod(f, 0o600)
	}
	again := &EventReturn{Server: shipper.Server, Returner: sink, OffsetFile: offsetFile, Batch: 10}
	runInBackground(t, again.Run)
	waitForTags(t, sink, 3)
}

// runInBackground runs a shipper until the test ends, and waits for it to
// stop before the test's temporary directory is removed.
//
// The shipper records its offset after it ships, so one still running when
// the test returns writes into a directory that is being removed, and the
// cleanup fails with "directory not empty": seen on CI for two tests of this
// file and for the reactor's (DIVERGENCE 5.519), each of which had started
// the shipper with `go` and a deferred cancel that did not wait.
func runInBackground(t *testing.T, run func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
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

// A returner that cannot be reached is a failure to ship, which is retried,
// and not an event dropped.
//
// It was counted in `halite_events_dropped_total{reason="returner_failed"}`,
// so `HaliteEventsDropped` told an operator the bus had dropped events when it
// had dropped none and the events were still there to be sent. DIVERGENCE
// 5.518.
func TestAReturnerOutageIsNotCountedAsDroppedEvents(t *testing.T) {
	sink := &recordingReturner{fails: true, failAt: 0}
	shipper, bus, _ := eventReturnLab(t, sink)
	srv := shipper.Server
	srv.Metrics = metrics.NewRegistry()
	if _, err := bus.Append(&eventbus.Event{Tag: "halite/job/x/new", Stamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_ = shipper.Run(ctx)

	var out bytes.Buffer
	if err := srv.Metrics.Write(&out); err != nil {
		t.Fatal(err)
	}
	expo := out.String()
	if !strings.Contains(expo, "\nhalite_event_returner_failures_total 1\n") {
		t.Errorf("the failed shipment was not counted as a returner failure:\n%s", grepLines(expo, "returner_fail"))
	}
	if strings.Contains(expo, `reason="returner_failed"`) {
		t.Errorf("a returner outage is still counted as dropped events:\n%s", grepLines(expo, "dropped"))
	}
}
