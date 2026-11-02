package main

import (
	"math/rand/v2"
	"time"
)

// reconnect is the pace of an agent's attempts to hold its stream to the hub,
// and the point at which `hub_tries` says to stop.
//
// `hub_tries` is documented as "reconnect attempts before giving up", and the
// loop counted every attempt the process had ever made: a node set to give up
// after five that held its stream for a month and then lost it five times in
// the following year stopped, with a healthy hub, having never failed to
// connect. The count is of attempts since one last succeeded. The same loop
// grew its backoff on every end and shortened it only on a clean one, so a
// node that had once been refused a few times waited a minute after every
// later drop however long the stream had stood before it, and every node of a
// fleet that lost the hub together waited exactly the same minute and came
// back together. A stream that was opened resets both, and the wait carries
// jitter. DIVERGENCE 5.546.
type reconnect struct {
	// tries is `hub_tries`; zero retries for ever.
	tries   int64
	attempt int64
	backoff time.Duration
	// jitter returns a number in [0, 1), a seam for the tests.
	jitter func() float64
}

const (
	minBackoff = time.Second
	maxBackoff = time.Minute
)

func newReconnect(tries int64) *reconnect {
	return &reconnect{tries: tries, backoff: minBackoff, jitter: rand.Float64}
}

// attempting records that an attempt is about to be made.
func (r *reconnect) attempting() { r.attempt++ }

// opened records that the hub accepted the stream: whatever came before it
// was a run of failures that has ended.
func (r *reconnect) opened() {
	r.attempt = 0
	r.backoff = minBackoff
}

// ended says what to do when the stream is gone, cleanly (the hub asked for a
// reconnection) or with an error. It answers how long to wait and whether to
// stop.
func (r *reconnect) ended(clean bool) (wait time.Duration, giveUp bool) {
	if clean {
		r.backoff = minBackoff
	}
	if r.tries > 0 && r.attempt >= r.tries {
		return 0, true
	}
	// Up to a fifth either side, so that nodes that lost the hub together do
	// not come back together.
	wait = time.Duration(float64(r.backoff) * (0.8 + 0.4*r.jitter()))
	if r.backoff < maxBackoff {
		r.backoff *= 2
		if r.backoff > maxBackoff {
			r.backoff = maxBackoff
		}
	}
	return wait, false
}
