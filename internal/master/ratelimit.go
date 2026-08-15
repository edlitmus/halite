package master

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// DefaultEnrollRate is how many enrollment requests one source address may
// make per minute. A host waiting to be accepted retries every ten seconds
// — six a minute — so this leaves room for several behind one NAT gateway
// while still costing an attacker a great deal of time: enrollment is the
// one route that verifies a signature before anyone has authenticated.
const DefaultEnrollRate = 60

// sweepAfter is how long an untouched bucket is kept. Buckets are cheap,
// but a control plane facing the internet would otherwise hold one per
// address that ever knocked.
const sweepAfter = 10 * time.Minute

// rateLimiter is a token bucket per source address. It is deliberately
// small: the pending cap is what bounds the disk, and this bounds how fast
// anyone can spend the control plane's time getting there.
type rateLimiter struct {
	perMinute int
	// now is the clock, so a test does not have to sleep.
	now func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute int) *rateLimiter {
	if perMinute <= 0 {
		perMinute = DefaultEnrollRate
	}
	return &rateLimiter{
		perMinute: perMinute,
		now:       time.Now,
		buckets:   map[string]*bucket{},
	}
}

// allow reports whether a request from addr may proceed, spending a token
// if it may. A full bucket holds one minute's worth, so a fleet coming up
// together is not turned away for arriving at once.
func (l *rateLimiter) allow(addr string) bool {
	key := sourceOf(addr)
	burst := float64(l.perMinute)
	perSecond := burst / 60

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)

	b, seen := l.buckets[key]
	if !seen {
		b = &bucket{tokens: burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * perSecond
	if b.tokens > burst {
		b.tokens = burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops buckets nobody has used lately. The caller holds the lock.
func (l *rateLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < sweepAfter {
		return
	}
	l.lastSweep = now
	for key, b := range l.buckets {
		if now.Sub(b.last) >= sweepAfter {
			delete(l.buckets, key)
		}
	}
}

// sourceOf is the address a request is counted against: the peer's IP,
// never a header. X-Forwarded-For is whatever the client typed, and the
// control plane terminates its own TLS, so there is no proxy to trust.
func sourceOf(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// limitEnrollment answers a caller that is asking too often, and reports
// whether it did.
func (s *Server) limitEnrollment(w http.ResponseWriter, r *http.Request) bool {
	if s.enrollLimit.allow(r.RemoteAddr) {
		return false
	}
	s.noteFlood(sourceOf(r.RemoteAddr))
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests,
		"too many enrollment requests from %s; try again in a minute", sourceOf(r.RemoteAddr))
	return true
}
