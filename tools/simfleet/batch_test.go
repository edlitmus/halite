package main

import (
	"strings"
	"testing"
	"time"
)

// Four slices of three, one after another, with a second between one
// slice's last return and the next slice's first arrival.
func TestBatchOfOneSliceAtATime(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	var recv, ret []int64
	for s := range 4 {
		start := time.Duration(s) * 2 * time.Second
		for i := range 3 {
			recv = append(recv, at(t0, start+time.Duration(i)*time.Millisecond))
			ret = append(ret, at(t0, start+time.Second))
		}
	}
	b := batchOf(3, 12, recv, ret)
	if b.Size != 3 || b.Slices != 4 || b.PeakInFlight != 3 {
		t.Fatalf("batch %+v; want size 3, 4 slices, 3 at once", b)
	}
	if b.Gaps == nil || b.Gaps.Count != 3 || b.Gaps.P50 != 1 || b.Gaps.Last != 1 {
		t.Fatalf("gaps %+v; want three of 1 s", b.Gaps)
	}
}

// The second slice delivered before the first returned: the peak says
// so, and no gaps are given, because slices that overlap have none.
func TestBatchOfSeesTwoSlicesInFlight(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	recv := []int64{at(t0, 0), at(t0, 0), at(t0, 100*time.Millisecond), at(t0, 100*time.Millisecond)}
	ret := []int64{at(t0, time.Second), at(t0, time.Second), at(t0, time.Second), at(t0, time.Second)}
	b := batchOf(2, 4, recv, ret)
	if b.PeakInFlight != 4 || b.Gaps != nil {
		t.Fatalf("batch %+v; want 4 in flight and no gaps", b)
	}
	var sb strings.Builder
	result{Fun: "test.ping", Batch: b}.print(&sb)
	if !strings.Contains(sb.String(), "MORE THAN ONE SLICE IN FLIGHT") {
		t.Fatalf("printed:\n%s", sb.String())
	}
}

// A node that received the job and never answered is not in flight for
// ever: counting it so would make every later slice an overlap.
func TestBatchOfLeavesOutANodeThatNeverAnswered(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	recv := []int64{at(t0, 0), at(t0, 0), at(t0, 5*time.Second), at(t0, 5*time.Second)}
	ret := []int64{at(t0, time.Second), 0, at(t0, 6*time.Second), at(t0, 6*time.Second)}
	if b := batchOf(2, 4, recv, ret); b.PeakInFlight != 2 {
		t.Fatalf("batch %+v; want at most 2 in flight", b)
	}
}

// A batched job has no SPEC 30 verdict: the rows are for a job sent to
// every node at once, and a batch is slower on purpose.
func TestABatchedJobHasNoVerdict(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	sub := submitFile{JID: "j", Fun: "test.ping", SubmitStart: t0, SubmitEnd: t0, Batch: 1}
	r := summarize(sub, 1, []int64{at(t0, time.Second)}, []int64{at(t0, 20*time.Second)}, 0, 1)
	if r.Verdict != nil || r.Batch == nil {
		t.Fatalf("verdict %+v, batch %+v; want no verdict and a batch", r.Verdict, r.Batch)
	}
}

func TestBatchFlagsRefuseWhatTheHubCannotBeSent(t *testing.T) {
	for _, tc := range []struct {
		spec          string
		wait, timeout time.Duration
		want          string
	}{
		{"", time.Second, 0, "only mean something with -batch"},
		{"10%", 1500 * time.Millisecond, 0, "whole number of seconds"},
		{"10%", 0, -time.Second, "negative"},
	} {
		b := batchFlags{spec: &tc.spec, wait: &tc.wait, timeout: &tc.timeout}
		if err := b.check(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q %s %s: err = %v, want %q", tc.spec, tc.wait, tc.timeout, err, tc.want)
		}
	}
	spec, wait, timeout := "10%", 2*time.Second, time.Minute
	req := batchFlags{spec: &spec, wait: &wait, timeout: &timeout}.request("sim-*", "test.ping")
	if req.Batch != "10%" || req.BatchWaitSeconds != 2 || req.BatchTimeoutSecs != 60 {
		t.Fatalf("request %+v", req)
	}
}

// The next slice delivered in the same clock tick as the last return of
// the one before it: one slice at a time, not two. The return is counted
// first at a tie, or every such hand-over would read as an overlap.
func TestBatchOfAHandOverInOneTick(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	recv := []int64{at(t0, 0), at(t0, 0), at(t0, time.Second), at(t0, time.Second)}
	ret := []int64{at(t0, 500*time.Millisecond), at(t0, time.Second), at(t0, 2*time.Second), at(t0, 2*time.Second)}
	if b := batchOf(2, 4, recv, ret); b.PeakInFlight != 2 {
		t.Fatalf("batch %+v; want at most 2 in flight", b)
	}
}
