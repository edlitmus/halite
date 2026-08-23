package eventbus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newBus(t *testing.T) *Bus {
	t.Helper()
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func put(t *testing.T, b *Bus, tag string, data map[string]any) string {
	t.Helper()
	offset, err := b.Append(&Event{Tag: tag, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return offset
}

// The property Salt's bus does not have: a subscriber that restarts
// resumes where it stopped, so a reactor restart is lossless and an
// incident can be reconstructed.
func TestASubscriberResumesFromAnOffset(t *testing.T) {
	b := newBus(t)
	for i := 0; i < 5; i++ {
		put(t, b, fmt.Sprintf("halite/job/2026082%d/new", i), map[string]any{"n": i})
	}

	first, next, err := b.Read(Earliest, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("read %d events", len(first))
	}
	rest, _, err := b.Read(next, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 3 {
		t.Fatalf("the rest is %d events", len(rest))
	}
	// No record is read twice and none is skipped. The payload comes
	// back as int64 rather than float64: a record read off the log is
	// in the nine-type model, not in whatever `encoding/json` guessed.
	seen := map[int64]bool{}
	for _, e := range append(first, rest...) {
		n, _ := e.Data["n"].(int64)
		if seen[n] {
			t.Errorf("event %v was read twice", n)
		}
		seen[n] = true
	}
	if len(seen) != 5 {
		t.Errorf("saw %d of 5 events", len(seen))
	}

	// `latest` skips what is already there, which is what a new
	// subscriber that only wants what happens next means.
	fresh, _, err := b.Read(Latest, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 0 {
		t.Errorf("`latest` returned %d past events", len(fresh))
	}
}

func TestTagGlobsSelectWhatASubscriberAskedFor(t *testing.T) {
	b := newBus(t)
	put(t, b, "halite/job/20260823T01/new", nil)
	put(t, b, "halite/job/20260823T01/ret/web1.example", nil)
	put(t, b, "halite/node/web1.example/start", nil)
	put(t, b, "halite/key/web1.example/accept", nil)

	cases := []struct {
		pattern string
		want    int
	}{
		{"", 4},
		{"*", 4},
		{"halite/job/*/new", 1},
		{"halite/job/**", 2},
		{"halite/node/**", 1},
		{"halite/key/*/accept", 1},
		{"halite/beacon/**", 0},
	}
	for _, c := range cases {
		var tags []string
		if c.pattern != "" {
			tags = []string{c.pattern}
		}
		got, _, err := b.Read(Earliest, tags, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != c.want {
			t.Errorf("%q matched %d events, want %d", c.pattern, len(got), c.want)
		}
	}
}

// Two events in the same millisecond are ordinary, and a timestamp
// that cannot tell them apart makes a log unreadable at exactly the
// moment it matters.
func TestTheStampCarriesMicroseconds(t *testing.T) {
	b := newBus(t)
	at := time.Date(2026, 8, 23, 14, 22, 11, 123456000, time.UTC)
	if _, err := b.Append(&Event{Tag: "halite/test", Stamp: at}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(b.Dir(), "00000001.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "2026-08-23T14:22:11.123456Z") {
		t.Errorf("the record reads %s", raw)
	}
	got, _, err := b.Read(Earliest, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Stamp.Equal(at) {
		t.Errorf("the stamp round trips to %s, not %s", got[0].Stamp, at)
	}
	if got[0].Schema != Schema {
		t.Errorf("the record carries schema %q", got[0].Schema)
	}
	if got[0].Offset == "" {
		t.Error("a record read back should carry the offset to resume from")
	}
}

// A tag reaches a log, a glob comparison, and an operator's terminal.
func TestAnUnusableTagIsRefused(t *testing.T) {
	b := newBus(t)
	for _, bad := range []string{"", "halite/../etc/passwd", "halite/\x00null", "halite/\x1b[2Jclear"} {
		if _, err := b.Append(&Event{Tag: bad}); err == nil {
			t.Errorf("%q was accepted as a tag", bad)
		}
	}
	if _, err := b.Append(&Event{Tag: strings.Repeat("a", 600)}); err == nil {
		t.Error("a 600-byte tag was accepted")
	}
}

func TestSegmentsRotateAndRetentionRemovesWholeOnes(t *testing.T) {
	b := newBus(t)
	b.SegmentBytes = 200
	for i := 0; i < 40; i++ {
		put(t, b, "halite/test", map[string]any{"i": i, "pad": strings.Repeat("x", 20)})
	}
	segments, err := b.segments()
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) < 3 {
		t.Fatalf("40 records at 200 bytes a segment produced %d segments", len(segments))
	}
	// Everything is still readable across the rotation.
	all, _, err := b.Read(Earliest, nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 40 {
		t.Errorf("read %d of 40 records across %d segments", len(all), len(segments))
	}

	// Size retention removes whole segments, oldest first, and never
	// the one being written.
	b.MaxBytes = 300
	removed, err := b.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if removed == 0 {
		t.Fatal("nothing was pruned under a 300-byte ceiling")
	}
	left, err := b.segments()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) == 0 {
		t.Fatal("everything was pruned, including the segment being written")
	}
	// The bus still works afterwards.
	if _, err := b.Append(&Event{Tag: "halite/test", Data: map[string]any{"after": true}}); err != nil {
		t.Fatal(err)
	}
}

// A record half-written by a crash must not end the read: everything
// after it is still good.
func TestATruncatedRecordDoesNotStopTheRest(t *testing.T) {
	b := newBus(t)
	put(t, b, "halite/one", nil)
	path := filepath.Join(b.Dir(), "00000001.ndjson")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString("{\"_tag\":\"halite/half\"\n")
	file.Close()
	put(t, b, "halite/three", nil)

	got, _, err := b.Read(Earliest, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d records around a torn one", len(got))
	}
	if got[1].Tag != "halite/three" {
		t.Errorf("the record after the torn one is %q", got[1].Tag)
	}
}

func TestAFollowerIsWokenByAnAppend(t *testing.T) {
	b := newBus(t)
	wait := b.Wait()
	go func() {
		time.Sleep(10 * time.Millisecond)
		b.Append(&Event{Tag: "halite/test"})
	}()
	select {
	case <-wait:
	case <-time.After(2 * time.Second):
		t.Fatal("a follower was not woken by an append")
	}
}

func TestABadOffsetIsRefusedRatherThanSilentlyStartingOver(t *testing.T) {
	b := newBus(t)
	put(t, b, "halite/test", nil)
	for _, bad := range []string{"nonsense", "1", "abc:def", "00000001:-4"} {
		if _, _, err := b.Read(bad, nil, 10); err == nil {
			t.Errorf("%q was accepted as an offset", bad)
		}
	}
}

// SPEC 6.4: a 64-bit integer survives the round trip. The bus is a file,
// so "the round trip" here includes being written and read back, and the
// standard decoder turns every number in a payload into a float64 —
// which changes the last digits of an identifier without saying so.
func TestASixtyFourBitIntegerSurvivesTheLog(t *testing.T) {
	b := newBus(t)
	const exact int64 = 9007199254740993
	put(t, b, "halite/audit/one", map[string]any{"build": exact})

	events, _, err := b.Read(Earliest, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("read %d events", len(events))
	}
	got, ok := events[0].Data["build"].(int64)
	if !ok {
		t.Fatalf("the payload came back as %T", events[0].Data["build"])
	}
	if got != exact {
		t.Errorf("wrote %d and read %d", exact, got)
	}
}
