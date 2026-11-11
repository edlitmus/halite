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
// decided it.
//
// Three outcomes an honest fleet makes over and over are not metered:
//
//   - a request whose bootstrap token would spend: a token-mode
//     bootstrap of a thousand nodes behind one NAT address must not be
//     metered, and a token that spends is the operator's decision made
//     in advance. Such a request is not charged on entry at all; see
//     below for why that, and not a refund, is what makes this true.
//   - a certificate issued, to a request that was charged: a node
//     collecting a certificate an operator accepted. The charge is
//     refunded once the certificate is issued.
//   - a pending answer for a record that was already there: `enroll
//     --wait` asks every five seconds until an operator accepts, and a
//     hundred nodes waiting behind one address would otherwise be
//     refused for waiting. Refunded likewise.
//
// What stays charged is what fills something: a new pending record, a
// refusal (a bad token, a name held by another key), and a malformed
// request. Every request but a token one is charged on entry, before
// the keystore sees it, and given the charge back afterwards for an
// outcome that is not metered. Charging on entry rather than after is
// what stops a burst of concurrent requests from one address all
// passing the check before any of them is counted.
//
// **A token request is not charged and then refunded, because that
// meters it after all** (DIVERGENCE 5.658). The comment above said so
// from the start and was true only one request at a time: a charge is
// held for as long as the request is in the keystore, an issue on AWS
// took about 345 ms, and 2,000 token enrolments from one address 64 at a
// time filled the 32-request budget with charges not yet refunded --
// 90 issued and 1,910 refused with 429 (5.279). So the hub asks first,
// through keystore.Authority.TokenAdmits, whether the request's token
// would spend for the node its CSR names, from this address, now; and
// if it would, and a use of it is free to hold (hold), the request goes
// in uncharged. If it then does not issue -- it lost the race for the
// token's last use, or its name is held by another key -- it is charged
// once that is known (charge).
//
// The alternatives, and why each is wrong:
//
//   - exempt any request that carries a token: a made-up token for a new
//     name still leaves a pending record, for an operator to accept by
//     hand, so a flood with a token field reaches the 1,024 pending
//     records exactly as 5.398's did without one.
//   - charge only after the outcome: a burst all passes the check before
//     any of it is counted, which is why the charge is taken on entry.
//   - a larger burst, or a refund that comes sooner: either moves the
//     concurrency at which a bootstrap is refused without removing it,
//     and a larger burst is room for a flood too.
//   - exempt a request whose token would spend, without hold: a token
//     with one use left, sent by a thousand requests at once, passes the
//     check a thousand times and spends once, and the rest each leave a
//     pending record nobody charged for. hold bounds the requests let
//     through uncharged on one token by the uses it has left, which the
//     operator chose; the rest are charged on entry like any other.
//     TestAContendedTokenCannotFillPendingUncharged is that case.
//
// Deciding whether a token would spend needs the body, so the body is
// now read before the budget is asked. A source over its budget used to
// cost a TLS handshake; it now costs a body of at most MaxEnrollBody
// read within enrollReadTimeout (5.338's bounds on any one request),
// and, for a request with a token in it, a hash and one file opened.
// TokenAdmits looks the token up before parsing the CSR so that a
// made-up one costs no more than that.
//
// The source is the connection's peer, as the login's is. The hub's
// port faces nodes directly and relays do not carry enrollment, so no
// header is trusted to say otherwise. An IPv6 peer is held by its /64,
// because one host is routinely given a whole /64.
type enrollLimit struct {
	mu      sync.Mutex
	sources map[string]*enrollBudget
	// held counts, per token identifier, the requests in flight that
	// were let through uncharged on the strength of that token; see
	// hold. Keys are tokens the hub minted, so the map is bounded by
	// the token store, and an entry goes when its count reaches zero.
	held map[string]int
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
	// metered at all, however many of its requests are in flight, so
	// long as the token has a use left for each (5.658).
	DefaultEnrollSourceRate  = 0.1
	DefaultEnrollSourceBurst = 32

	// enrollSources bounds the table, whose keys come from the network.
	// A full table gives up the source with the least spent, after
	// dropping every source that has drained to nothing, which is what
	// a source never seen would have.
	enrollSources = 4096
)

func newEnrollLimit() *enrollLimit {
	return &enrollLimit{sources: map[string]*enrollBudget{}, held: map[string]int{}}
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

// charge adds one request to source's level without asking whether it
// fits, for a request that was let through uncharged on its token and
// then did not issue. It may take the level over the burst, which only
// makes the source wait longer: the request has already been served,
// and what it cost is what the level is for.
func (l *enrollLimit) charge(source string, now time.Time, rate float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.sources[source]
	if !ok {
		if len(l.sources) >= enrollSources {
			l.evict(now, rate)
		}
		b = &enrollBudget{at: now}
		l.sources[source] = b
	}
	b.level, b.at = b.drained(now, rate)+1, now
}

// hold takes one of a token's left uses for a request about to be let
// through uncharged, and reports false when every use left is already
// held by a request in flight. The caller gives it back with unhold
// once Enroll has returned, by which time a use that was spent is in
// the token's own count.
//
// This is what makes the exemption safe. Without it, a token with one
// use left sent by a thousand requests at once passes TokenAdmits a
// thousand times, spends once, and the rest -- a token that does not
// spend still records the request for an operator -- each leave a
// pending record nobody charged for, which is the flood DIVERGENCE 5.398
// closed, reopened for anybody holding a nearly spent token. With it,
// the requests let through uncharged on one token never outnumber its
// uses, which the operator chose when minting it; the rest are charged
// on entry like any other.
func (l *enrollLimit) hold(tokenID string, left int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[tokenID] >= left {
		return false
	}
	l.held[tokenID]++
	return true
}

func (l *enrollLimit) unhold(tokenID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[tokenID]--; l.held[tokenID] <= 0 {
		delete(l.held, tokenID)
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
