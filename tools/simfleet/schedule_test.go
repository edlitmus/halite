package main

import (
	"testing"
	"time"
)

// An event the posters could not take is counted against whoever was not
// keeping up. Taken on a tick that came on time, it was the posters; on a
// tick that came after the harness had not run for 600 ms, it was the
// harness, which until 5.691 the run also blamed on the posters.
func TestAnEventNotOfferedSaysWhetherTheHarnessWasLate(t *testing.T) {
	start := time.Unix(0, 0)
	refuseAll := func(int64) bool { return false }
	cases := map[string]struct {
		ticks          []time.Duration // after start
		wantBusy       int64
		wantLate       int64
		wantLongestGap time.Duration
	}{
		"posters busy, harness on time": {
			ticks:    []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond, 50 * time.Millisecond},
			wantBusy: 2, wantLongestGap: 10 * time.Millisecond,
		},
		"the harness stopped for 600 ms": {
			ticks:    []time.Duration{10 * time.Millisecond, 610 * time.Millisecond},
			wantLate: 24, wantLongestGap: 600 * time.Millisecond,
		},
		"a tick exactly lateTicks periods late is on time": {
			ticks:    []time.Duration{50 * time.Millisecond},
			wantBusy: 2, wantLongestGap: 50 * time.Millisecond,
		},
	}
	for name, c := range cases {
		s := schedule{start: start, last: start, period: 10 * time.Millisecond, rate: 40, d: 2 * time.Second}
		for _, at := range c.ticks {
			s.tick(start.Add(at), refuseAll)
		}
		if s.busy != c.wantBusy || s.late != c.wantLate || s.offered != 0 || s.longestGap != c.wantLongestGap {
			t.Errorf("%s: busy %d, late %d, offered %d, longest gap %s; want busy %d, late %d, gap %s",
				name, s.busy, s.late, s.offered, s.longestGap, c.wantBusy, c.wantLate, c.wantLongestGap)
		}
	}
}

// What a poster takes is offered whether or not the tick was late, and
// the schedule ends at -duration with every event of it due once.
func TestTheScheduleOffersEachDueEventOnceAndEndsOnTime(t *testing.T) {
	start := time.Unix(0, 0)
	var seen []int64
	takeAll := func(seq int64) bool { seen = append(seen, seq); return true }
	s := schedule{start: start, last: start, period: 10 * time.Millisecond, rate: 40, d: 2 * time.Second}
	if s.tick(start.Add(700*time.Millisecond), takeAll) {
		t.Fatal("the schedule ended at 0.7 s of 2 s")
	}
	if !s.tick(start.Add(2500*time.Millisecond), takeAll) {
		t.Fatal("the schedule had not ended at 2500 ms of 2 s")
	}
	if s.offered != 80 || s.busy != 0 || s.late != 0 || len(seen) != 80 {
		t.Fatalf("offered %d, busy %d, late %d, posters saw %d; want 80 offered and seen", s.offered, s.busy, s.late, len(seen))
	}
	for i, seq := range seen {
		if seq != int64(i) {
			t.Fatalf("event %d offered as seq %d", i, seq)
		}
	}
}
