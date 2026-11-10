// Package nodelink is how a node holds its link to the hub: the pace of
// its reconnects, and how it retries a return the hub did not take.
//
// It is shared by halite-node and simfleet, which simulates thousands of
// nodes against one hub to measure SPEC 30's rows. The two used to have
// their own copies: simfleet reconnected every second where a node backs
// off to a minute with jitter, and gave up on a return at its first
// failure where a node tries five times. So a reconnect storm from
// simfleet was harsher and more synchronised than a fleet's, and near
// saturation a fleet adds load that simfleet took away (DIVERGENCE
// 5.642, limits L3 and L4 of docs/simfleet-aws-plan.md). One copy means a
// change to the node's behaviour is a change to what simfleet measures.
package nodelink

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

// Reconnect is the pace of an agent's attempts to hold its stream to the hub,
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
type Reconnect struct {
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

// NewReconnect is a pacer that gives up after tries failures in a row;
// zero is never.
func NewReconnect(tries int64) *Reconnect {
	return &Reconnect{tries: tries, backoff: minBackoff, jitter: cryptoFloat}
}

// Attempting records that an attempt is about to be made.
func (r *Reconnect) Attempting() { r.attempt++ }

// Opened records that the hub accepted the stream: whatever came before it
// was a run of failures that has ended.
func (r *Reconnect) Opened() {
	r.attempt = 0
	r.backoff = minBackoff
}

// Ended says what to do when the stream is gone, cleanly (the hub asked for a
// reconnection) or with an error. It answers how long to wait and whether to
// stop.
func (r *Reconnect) Ended(clean bool) (wait time.Duration, giveUp bool) {
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

// Attempts is how many attempts have been made since a stream last
// opened.
func (r *Reconnect) Attempts() int64 { return r.attempt }

// cryptoFloat is a number in [0, 1) from crypto/rand: SPEC 25.3 admits no
// other source in this tree, and the jitter is not worth an exception. If the
// system's source fails the wait is the middle of the spread, which is the
// backoff itself.
func cryptoFloat() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	return float64(binary.LittleEndian.Uint64(b[:])>>11) / (1 << 53)
}
