package value

import (
	"math"
	"testing"
)

// ExactInt64 must give the same answer on every architecture, which a
// round trip through int64 does not: Go leaves an out-of-range float
// conversion to the implementation. The cases that matter are the edges,
// so the table is mostly edges. DIVERGENCE 5.741.
func TestExactInt64TakesOnlyWholeNumbersInRange(t *testing.T) {
	twoTo63 := math.Ldexp(1, 63)
	cases := []struct {
		in   float64
		want int64
		ok   bool
	}{
		{0, 0, true},
		{math.Copysign(0, -1), 0, true},
		{1000, 1000, true},
		{-1, -1, true},
		{1000.7, 0, false},
		{-1.5, 0, false},
		{math.NaN(), 0, false},
		{math.Inf(1), 0, false},
		{math.Inf(-1), 0, false},
		// -2^63 is MinInt64 exactly, and is in range.
		{-twoTo63, math.MinInt64, true},
		// 2^63 is one past MaxInt64. float64(math.MaxInt64) rounds up to
		// it, which is why the round-trip test accepted it on arm64,
		// where the conversion saturates.
		{twoTo63, 0, false},
		{float64(math.MaxInt64), 0, false},
		// The largest float below 2^63 is a whole number in range.
		{math.Nextafter(twoTo63, 0), 1<<63 - 1024, true},
		{math.Nextafter(-twoTo63, math.Inf(-1)), 0, false},
		{1e19, 0, false},
		{-1e19, 0, false},
	}
	for _, c := range cases {
		got, ok := ExactInt64(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ExactInt64(%v) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
