package ratelimit

import (
	"testing"
	"time"
)

var start = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func takes(b *Bucket, now time.Time, rate float64, burst, n int) int {
	got := 0
	for i := 0; i < n; i++ {
		if b.Take(now, rate, burst) {
			got++
		}
	}
	return got
}

func TestANewBucketIsFull(t *testing.T) {
	var b Bucket
	if got := takes(&b, start, 1, 5, 10); got != 5 {
		t.Fatalf("a new bucket of 5 gave %d at once", got)
	}
}

func TestABucketRefillsAtItsRate(t *testing.T) {
	var b Bucket
	takes(&b, start, 2, 4, 4)
	if b.Take(start, 2, 4) {
		t.Fatal("an empty bucket gave a token")
	}
	// Two a second, for a second and a half: three.
	if got := takes(&b, start.Add(1500*time.Millisecond), 2, 4, 10); got != 3 {
		t.Fatalf("1.5s at 2/s refilled %d, want 3", got)
	}
}

func TestABucketNeverHoldsMoreThanItsBurst(t *testing.T) {
	var b Bucket
	takes(&b, start, 1, 3, 3)
	if got := takes(&b, start.Add(time.Hour), 1, 3, 10); got != 3 {
		t.Fatalf("an hour idle gave %d, want the burst of 3", got)
	}
}

func TestABurstBelowOneIsOne(t *testing.T) {
	var b Bucket
	if got := takes(&b, start, 1, 0, 5); got != 1 {
		t.Fatalf("a burst of 0 gave %d at once, want 1", got)
	}
	if !b.Take(start.Add(time.Second), 1, 0) {
		t.Fatal("a burst of 0 never refilled")
	}
}

// A reload that lowers a limit applies to the bucket already in use: it
// caps at the new burst on the next Take, rather than spending what it
// held under the old one.
func TestANewLimitAppliesToABucketInUse(t *testing.T) {
	var b Bucket
	b.Take(start, 1, 100)
	if got := takes(&b, start, 1, 2, 10); got != 2 {
		t.Fatalf("after lowering the burst to 2, it gave %d", got)
	}
}
