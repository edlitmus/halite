package value

import "math"

// ExactInt64 converts f to an int64 when it is a whole number int64 can
// hold, and reports false for everything else: a fraction, NaN, either
// infinity, or a magnitude beyond int64's range.
//
// The obvious test -- `f == float64(int64(f))` -- is not one. Go leaves
// the conversion of an out-of-range float to an integer to the
// implementation, and the two tier-1 architectures disagree: arm64
// saturates, amd64 returns math.MinInt64. 2^63 then round-trips on arm64
// (float64(MaxInt64) rounds up to 2^63) and so was accepted there as
// MaxInt64, and refused on amd64. The range is checked before the
// conversion, so the conversion only ever sees a value it is defined for.
//
// The bounds are exact in float64: -2^63 is MinInt64, and 2^63 is the
// first value past MaxInt64. NaN fails the Trunc comparison, and the
// infinities fail the range.
func ExactInt64(f float64) (int64, bool) {
	if f != math.Trunc(f) || f < -(1<<63) || f >= 1<<63 {
		return 0, false
	}
	return int64(f), true
}
