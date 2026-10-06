// Package ratelimit is the token bucket the hub and the API share.
//
// There were three copies of it -- the reactor's per-glob limit, the
// webhook receiver's per-path limit, and the hub's per-node limit on
// evidence-head reports -- written at different times and identical to
// the line. Three copies of a rule are three places for it to drift, and
// a bucket that drifts fails quietly: it lets a flood through, or turns
// away an honest caller, and nothing says which. DIVERGENCE 5.233.
//
// It holds no lock. Every caller already keeps the bucket beside other
// per-key state under a lock of its own, and a lock in here would be a
// second one to take in the right order.
package ratelimit

import "time"

// Bucket is one token bucket. The zero value is ready to use, and is
// full the first time it is asked.
//
// The rate and burst are passed to each Take rather than kept, because
// they come from configuration a reload can change while the bucket
// lives on: the next Take refills at the new rate and caps at the new
// burst, which is what an operator who lowered a limit expects.
type Bucket struct {
	tokens float64
	filled time.Time
}

// Take refills the bucket for the time since it was last asked, at rate
// tokens a second up to burst, then takes one token if there is one, and
// says whether there was.
//
// A burst below one is taken as one: a bucket that can never hold a
// whole token refuses everything, which no configuration means.
func (b *Bucket) Take(now time.Time, rate float64, burst int) bool {
	capacity := float64(burst)
	if capacity < 1 {
		capacity = 1
	}
	if b.filled.IsZero() {
		b.tokens, b.filled = capacity, now
	}
	b.tokens += now.Sub(b.filled).Seconds() * rate
	if b.tokens > capacity {
		b.tokens = capacity
	}
	b.filled = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
