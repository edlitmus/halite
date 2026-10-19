package hub

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// enrollLimit is the per-source half of issue 321's M-06, which 5.338
// left: how fast one address may spend the enrollment bounds.
//
// `POST /v1/enroll` is the one route that reads the input of a caller
// with no certificate. 5.338 bounded what one request may cost (the body, the CSR,
// the read) and what all of them may hold (1,024 pending records), but
// not how fast one caller may reach the second bound. On beastie one
// address made 1,024 names pending in 30 s, sixteen requests at a time,
// and a real node from anywhere was then refused until an operator
// cleared them; this held the same flood to 32 (DIVERGENCE 5.398).
//
// It is a leaky budget, as the API's login source budget is (5.304): a
// level that rises by one for each request that is charged and drains
// at the rate, refused with 429 when it is full. It is not
// internal/ratelimit.Bucket for the reason loginBudget is not: whether a
// request should be charged is known only after the keystore has
// decided it. So admit charges on entry, before the body is read, and
// refund gives the charge back for the two outcomes an honest fleet
// makes over and over:
//
//   - a certificate issued: a token-mode bootstrap of a thousand nodes
//     behind one NAT address must not be metered, and a token that
//     spends is the operator's decision made in advance;
//   - a pending answer for a record that was already there: `enroll
//     --wait` asks every five seconds until an operator accepts, and a
//     hundred nodes waiting behind one address would otherwise be
//     refused for waiting.
//
// What stays charged is what fills something: a new pending record, a
// refusal (a bad token, a name held by another key), and a malformed
// request. Charging on entry rather than after is what stops a burst of
// concurrent requests from one address all passing the check before any
// of them is counted.
//
// The source is the connection's peer, as the login's is. The hub's
// port faces nodes directly and relays do not carry enrollment, so no
// header is trusted to say otherwise. An IPv6 peer is held by its /64,
// because one host is routinely given a whole /64.
type enrollLimit struct {
	mu      sync.Mutex
	sources map[string]*enrollBudget
}

type enrollBudget struct {
	level float64
	at    time.Time
}

const (
	// DefaultEnrollSourceRate and DefaultEnrollSourceBurst are
	// enroll_source_rate and enroll_source_burst. Thirty-two at once is
	// room for a rack being racked behind one address in manual mode;
	// one every ten seconds after that is 8,640 a day, so one address
	// takes nearly three hours to fill the 1,024 pending
	// records, where it took thirty seconds. Both are a judgement, not a
	// measurement of any estate, and a token-mode bootstrap is not
	// metered at all.
	DefaultEnrollSourceRate  = 0.1
	DefaultEnrollSourceBurst = 32

	// enrollSources bounds the table, whose keys come from the network.
	// A full table gives up the source with the least spent, after
	// dropping every source that has drained to nothing, which is what
	// a source never seen would have.
	enrollSources = 4096
)

func newEnrollLimit() *enrollLimit {
	return &enrollLimit{sources: map[string]*enrollBudget{}}
}

// enrollSource is the address an enrollment is held to.
func enrollSource(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.Is6() {
		if prefix, err := addr.WithZone("").Prefix(64); err == nil {
			return prefix.String()
		}
	}
	return addr.String()
}

// drained is b's level at now, at rate a second.
func (b *enrollBudget) drained(now time.Time, rate float64) float64 {
	level := b.level - now.Sub(b.at).Seconds()*rate
	if level < 0 {
		level = 0
	}
	return level
}

// admit charges source one request, or reports how long it must wait
// before one would be admitted.
func (l *enrollLimit) admit(source string, now time.Time, rate float64, burst int) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	capacity := float64(max(burst, 1))
	b, ok := l.sources[source]
	if !ok {
		if len(l.sources) >= enrollSources {
			l.evict(now, rate)
		}
		b = &enrollBudget{at: now}
		l.sources[source] = b
	}
	level := b.drained(now, rate)
	if level+1 > capacity {
		// Until one whole request has drained, rounded up to the second
		// Retry-After can say.
		wait := time.Duration((level + 1 - capacity) / rate * float64(time.Second))
		return max(wait.Round(time.Second), time.Second)
	}
	b.level, b.at = level+1, now
	return 0
}

// refund gives back the charge admit took, for an outcome that is not
// metered.
func (l *enrollLimit) refund(source string, now time.Time, rate float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.sources[source]; ok {
		b.level, b.at = math.Max(b.drained(now, rate)-1, 0), now
	}
}

// evict drops every source that has drained to nothing, and if none had,
// the one with the least spent. The caller holds the lock.
func (l *enrollLimit) evict(now time.Time, rate float64) {
	least, leastLevel := "", math.Inf(1)
	for source, b := range l.sources {
		level := b.drained(now, rate)
		if level == 0 {
			delete(l.sources, source)
			continue
		}
		if level < leastLevel {
			least, leastLevel = source, level
		}
	}
	if len(l.sources) >= enrollSources && least != "" {
		delete(l.sources, least)
	}
}

func (s *Server) enrollSourceRate() float64 {
	if s.EnrollSourceRate > 0 {
		return s.EnrollSourceRate
	}
	return DefaultEnrollSourceRate
}

func (s *Server) enrollSourceBurst() int {
	if s.EnrollSourceBurst > 0 {
		return s.EnrollSourceBurst
	}
	return DefaultEnrollSourceBurst
}
