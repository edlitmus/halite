package eventbus

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/chaos"
)

// SPEC 31's chaos layer: the event bus at its retention limit.
//
// `TestSegmentsRotateAndRetentionRemovesWholeOnes` already covers that
// pruning happens. What this adds is the two questions the chaos row
// actually asks — what an *appender* experiences when the bus is full,
// and what a *reader* experiences when the history it was pointing at
// has gone — and the second answer is not the one this test was written
// expecting.
func TestChaosEventBusAtRetentionLimit(t *testing.T) {
	s := chaos.Exercises(chaos.EventBusAtRetention)
	t.Logf("%s\n  defined: %s\n  not established: %s", s.Key, s.Behaviour, s.Limit)

	dir := t.TempDir()
	bus, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	// Small enough that a few hundred events roll through many
	// segments, and a size limit that keeps only a couple of them.
	bus.SegmentBytes = 1024
	bus.MaxBytes = 4096
	bus.Retention = 0

	// Appending never blocks and never fails because the bus is full.
	// The limit is enforced by discarding history, not by refusing the
	// present: an estate whose event bus stopped accepting events at a
	// threshold would go blind at exactly the moment it was busiest.
	const appends = 400
	var first, last string
	for i := 0; i < appends; i++ {
		offset, err := bus.Append(&Event{
			Tag:  "halite/chaos/full",
			Data: map[string]any{"n": i, "pad": strings.Repeat("x", 64)},
		})
		if err != nil {
			t.Fatalf("append %d failed with the bus at its limit: %v", i, err)
		}
		if i == 0 {
			first = offset
		}
		last = offset
		if _, err := bus.Prune(); err != nil {
			t.Fatalf("pruning at append %d: %v", i, err)
		}
	}

	segments, err := bus.segments()
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) < 1 {
		t.Fatal("pruning removed every segment, including the one being written")
	}
	var total int64
	for _, seq := range segments {
		info, err := os.Stat(bus.path(seq))
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	t.Logf("after %d appends: %d segments, %d bytes, limit %d",
		appends, len(segments), total, bus.MaxBytes)
	if total > bus.MaxBytes+bus.SegmentBytes {
		t.Errorf("the bus holds %d bytes against a limit of %d; pruning is by whole "+
			"segments, so it should be over by at most one segment (%d)",
			total, bus.MaxBytes, bus.SegmentBytes)
	}

	// The newest event is still there. Discarding the oldest is the
	// whole design, and discarding the newest would make the bus useless
	// under exactly the load that fills it.
	events, _, err := bus.Read(last, nil, 10)
	if err != nil {
		t.Errorf("the newest event's own offset was refused: %v", err)
	}
	_ = events

	// And now the question the chaos row is really asking: a reader
	// holding an offset into a segment that has been pruned.
	//
	// It is refused, by name, with how far behind it fell and where to
	// resume to lose the least. That is SPEC 17.2's `subscriber_lag`,
	// and until it was implemented this call returned events from the
	// oldest surviving segment with no indication that anything had
	// been missed -- 380 of them, measured by the first version of this
	// test. DIVERGENCE 4.12.
	behind, _, err := bus.Read(first, nil, 10)
	if err == nil {
		t.Fatalf("reading from a pruned offset returned %d events and no error; a "+
			"subscriber that has fallen off the back of the bus was silently "+
			"advanced, which is what SPEC 17.2 names Salt's bus for doing",
			len(behind))
	}
	if !errors.Is(err, ErrSubscriberLag) {
		t.Fatalf("a pruned offset gave %v, want a subscriber_lag", err)
	}
	var lag *LagError
	if !errors.As(err, &lag) {
		t.Fatalf("the error carries no detail: %v", err)
	}
	if lag.From != first {
		t.Errorf("the error names %q as the offset asked for, want %q", lag.From, first)
	}
	if lag.Segments < 1 {
		t.Errorf("the error says %d segments were pruned", lag.Segments)
	}
	// Oldest is the point of it: "your offset is gone" without somewhere
	// to resume cannot be acted on.
	resumed, _, err := bus.Read(lag.Oldest, nil, 10)
	if err != nil {
		t.Fatalf("the offset the lag error names as the oldest was itself refused: %v", err)
	}
	if len(resumed) == 0 {
		t.Error("resuming from the offset the error named returned nothing")
	}
	t.Logf("refused: %v", err)

	// A malformed offset is refused, which is the case that *is*
	// distinguished — so the silence above is about a valid offset
	// whose data has gone, not about parsing.
	if _, _, err := bus.Read("not-an-offset", nil, 10); err == nil {
		t.Error("a malformed offset was accepted")
	}
}

// Retention by age removes whole segments and leaves the one being
// written, however old it is.
//
// The segment in hand is never a candidate: pruning it would discard
// events that have just been accepted, which is the one thing a bus at
// its limit must not do.
func TestChaosEventBusRetentionKeepsTheSegmentInHand(t *testing.T) {
	s := chaos.Exercises(chaos.EventBusAtRetention)
	t.Logf("%s: retention by age", s.Key)

	bus, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	bus.SegmentBytes = 512
	bus.Retention = time.Hour
	bus.MaxBytes = 0
	// The clock is moved rather than the retention window shrunk to a
	// nanosecond. A nanosecond window depends on every segment's
	// modification time being measurably in the past, and a filesystem's
	// timestamp granularity is not measured in nanoseconds — Windows
	// hands out the same tick to files written milliseconds apart, so
	// the segment rotated just before the current one was inside the
	// window and survived. This test then reported two segments where it
	// wanted one, on CI, on a machine slow enough to make the write and
	// the check land in different ticks.
	//
	// Which is this session's first defect all over again: a test that
	// asserts something about the clock rather than about the code. The
	// bus has a clock hook; using it makes granularity irrelevant.
	now := time.Now()
	bus.Now = func() time.Time { return now.Add(2 * time.Hour) }

	for i := 0; i < 200; i++ {
		if _, err := bus.Append(&Event{
			Tag:  "halite/chaos/old",
			Data: map[string]any{"n": i, "pad": strings.Repeat("y", 64)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Every segment is older than a nanosecond, so only the one being
	// written can survive.
	removed, err := bus.Prune()
	if err != nil {
		t.Fatal(err)
	}
	segments, err := bus.segments()
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 {
		t.Errorf("retention left %d segments, want the one being written; removed %d",
			len(segments), removed)
	}
	if _, err := bus.Append(&Event{Tag: "halite/chaos/after", Data: map[string]any{"n": -1}}); err != nil {
		t.Errorf("appending after retention removed everything failed: %v", err)
	}
	got, _, err := bus.Read(Earliest, []string{"halite/chaos/after"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("%d events after a full prune, want the one just appended", len(got))
	}
	if len(got) == 1 && fmt.Sprint(got[0].Tag) != "halite/chaos/after" {
		t.Errorf("read back %q", got[0].Tag)
	}
}
