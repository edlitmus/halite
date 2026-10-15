package api

import (
	"net/http"
	"net/netip"
	"runtime"
	"sync"
	"time"
)

// loginLimit is what stands between `POST /v1/login` and anybody who
// can reach it: three limits, for the things an unauthenticated caller
// can spend there.
//
// The endpoint takes no credential before the password, because it is
// how somebody with no token gets one. Every failed local login runs a
// PBKDF2-HMAC-SHA-512 at the enforced minimum of 210,000 iterations --
// measured at 50 ms of CPU on the development Mac -- and runs it for an
// unknown name as well, so that the two take the same time. Nothing
// bounded either. So a caller could guess at any account about twenty
// times a second per core, and enough callers at once could fill every
// core with hashing and starve the rest of the service. SPEC 25.1's
// "unauthenticated network attacker" row named the hub's port, where
// mutual TLS stops one before any application code; it said nothing
// about this one. DIVERGENCE 5.232.
//
// checking bounds the CPU: password checks in flight at once, past
// which a login is refused with 503 rather than queued, because a queue
// is the flood held in memory instead of on the CPU.
//
// The backoff bounds the guessing at any one name from any one source:
// after a failure the pair is refused with 429 for a window that
// doubles, one second, two, four, up to fifteen minutes, and a success
// clears it. A refusal inside the window costs no hash and does not
// lengthen it.
//
// It was keyed by the name alone until DIVERGENCE 5.290, and that made
// it a lockout after all. The window is checked before the password, so
// one failure from anywhere each time the window opened -- about 105
// requests a day, timed by the Retry-After the hub itself sent -- kept a
// name refused all day, for its owner too, and the refusals cost the
// attacker nothing. Keyed by (source, name), the same attacker keeps
// only itself out: the owner's own address has no window. A right
// password from inside the window is still refused, from that source
// only, because answering it would make the backoff a free check of
// each guess.
//
// The price is that a guesser with many addresses is held per address
// rather than per name. Any limit on a name that holds across sources
// is a limit a third party can trip for its owner, which is the lockout
// this replaced; the per-source budget below is what bounds a sprayer
// instead.
//
// sources bounds the guessing from one source across every name, which
// the per-name backoff never did: the same address cycling through a
// thousand names had a thousand fresh windows. It is a leaky budget of
// failures, loginSourceBurst deep and draining at one every
// loginSourceDrain, checked before anything is looked up or hashed.
// Only failures fill it, so an operator who logs in correctly from a
// shared address costs it nothing, and a success does not empty it --
// a sprayer who knows one password could otherwise reset its own
// budget at will.
//
// Every name is held to the backoff, whether an account by that name
// exists or not. Holding only the real ones would make the 429 an
// oracle for which names are real, which the login's single failure
// message exists to deny.
type loginLimit struct {
	checking chan struct{}

	mu      sync.Mutex
	names   map[string]*loginBackoff
	sources map[string]*loginBudget
}

type loginBackoff struct {
	failures int
	until    time.Time
}

// loginBudget is a source's failures, draining with time. It is not
// internal/ratelimit.Bucket because that type's Take checks and spends
// in one step, and a login must check before it knows whether there is
// anything to spend: only a failure spends, and a failure is known only
// after the hash. Spending on every attempt instead would charge an
// operator's correct logins to the same budget as a sprayer's guesses.
type loginBudget struct {
	level float64
	at    time.Time
}

const (
	loginBackoffMax = 15 * time.Minute
	// loginBackoffNames bounds the table of (source, name) pairs, which
	// come from the caller. When it is full, the entry whose window ends
	// soonest goes: a flood of new names, each one second into its
	// window, is evicted before a pair somebody has been guessing at for
	// fifteen minutes, so the flood cannot be used to clear the pair it
	// is aimed at.
	loginBackoffNames = 4096
	// loginSources bounds the table of sources the same way, and a full
	// one gives up the source with the least spent.
	loginSources = 4096
	// loginSourceBurst is how many failures a source may have in hand
	// before it is refused; loginSourceDrain is how long each takes to
	// drain. Thirty is room for an office behind one NAT address to
	// mistype a good many passwords; one every thirty seconds is 2,880
	// guesses a day per source, against the unbounded spray this
	// replaced. Both are a judgement, not a measurement of any estate.
	loginSourceBurst = 30
	loginSourceDrain = 30 * time.Second
	// loginNameMax bounds a username before anything stores it, logs it,
	// or builds a key from it. No directory or account name anywhere
	// near it is plausible; a megabyte of name was a megabyte held in
	// the backoff table and written to the log for every request.
	loginNameMax = 256
)

func newLoginLimit() *loginLimit {
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	return &loginLimit{
		checking: make(chan struct{}, n),
		names:    map[string]*loginBackoff{},
		sources:  map[string]*loginBudget{},
	}
}

// loginSource is the address a login is held to.
//
// It is the connection's peer and nothing the request says about
// itself. The API is documented to face its callers directly -- a
// reverse proxy is optional (handbook chapter 32) -- and there is no
// setting naming a proxy to trust, so X-Forwarded-For is a header any
// caller can write: honouring it would let an attacker choose the source
// it is held to, which is to say not be held. Behind a proxy every
// caller is the proxy's address. The backoff then falls back to what it
// was, per name, and the source budget becomes one budget for everybody
// behind it -- said in DIVERGENCE 5.290 rather than worked around.
//
// An IPv6 peer is held by its /64. A single host is routinely given a
// whole /64, so per address it would have 2^64 fresh budgets.
func loginSource(r *http.Request) string {
	host := remoteHost(r)
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.Is6() {
		prefix, err := addr.WithZone("").Prefix(64)
		if err == nil {
			return prefix.String()
		}
	}
	return addr.String()
}

// loginKey is the backoff's key for a name tried from a source by a
// method. The NUL cannot occur in a source, so no pair of source and name
// can be spelled as another.
func loginKey(method, source, name string) string {
	return method + ":" + source + "\x00" + name
}

// wait reports how long key must wait before it may try again, or zero.
func (l *loginLimit) wait(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.names[key]
	if !ok || !now.Before(b.until) {
		return 0
	}
	return b.until.Sub(now)
}

// failed starts or doubles key's window.
func (l *loginLimit) failed(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.names[key]
	if !ok {
		if len(l.names) >= loginBackoffNames {
			l.evict(now)
		}
		b = &loginBackoff{}
		l.names[key] = b
	} else if !now.Before(b.until.Add(loginBackoffMax)) {
		// Quiet for a whole maximum window since the last one ended:
		// start again at one second, as a key never seen would.
		b.failures = 0
	}
	b.failures++
	window := time.Second << min(b.failures-1, 10)
	if window > loginBackoffMax {
		window = loginBackoffMax
	}
	b.until = now.Add(window)
}

// succeeded clears key's window.
func (l *loginLimit) succeeded(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.names, key)
}

// evict drops what has expired, and if nothing had, the entry whose
// window ends soonest. The caller holds the lock.
func (l *loginLimit) evict(now time.Time) {
	var soonest string
	var soonestAt time.Time
	found := false
	for key, b := range l.names {
		if !now.Before(b.until.Add(loginBackoffMax)) {
			// Expired long enough ago that a failure now would start
			// again at one second anyway.
			delete(l.names, key)
			continue
		}
		if !found || b.until.Before(soonestAt) {
			soonest, soonestAt, found = key, b.until, true
		}
	}
	if len(l.names) >= loginBackoffNames && found {
		delete(l.names, soonest)
	}
}

// drained is b's level at now. The caller holds the lock.
func (b *loginBudget) drained(now time.Time) float64 {
	level := b.level - float64(now.Sub(b.at))/float64(loginSourceDrain)
	if level < 0 {
		level = 0
	}
	return level
}

// sourceWait reports how long source must wait before it may try any
// name again, or zero.
func (l *loginLimit) sourceWait(source string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.sources[source]
	if !ok {
		return 0
	}
	over := b.drained(now) - (loginSourceBurst - 1)
	if over <= 0 {
		return 0
	}
	return time.Duration(over * float64(loginSourceDrain))
}

// sourceFailed spends one of source's failures.
func (l *loginLimit) sourceFailed(source string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.sources[source]
	if !ok {
		if len(l.sources) >= loginSources {
			l.evictSource(now)
		}
		b = &loginBudget{}
		l.sources[source] = b
	}
	b.level = b.drained(now) + 1
	b.at = now
}

// evictSource drops the sources that have drained, and if none had, the
// one with the least spent: a flood from fresh addresses goes before the
// address that has spent its budget. The caller holds the lock.
func (l *loginLimit) evictSource(now time.Time) {
	var least string
	var leastLevel float64
	found := false
	for source, b := range l.sources {
		level := b.drained(now)
		if level == 0 {
			delete(l.sources, source)
			continue
		}
		if !found || level < leastLevel {
			least, leastLevel, found = source, level, true
		}
	}
	if len(l.sources) >= loginSources && found {
		delete(l.sources, least)
	}
}

// acquire takes a password-check slot, or reports that there was none.
func (l *loginLimit) acquire() bool {
	select {
	case l.checking <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *loginLimit) release() { <-l.checking }
