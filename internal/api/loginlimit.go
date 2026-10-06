package api

import (
	"runtime"
	"sync"
	"time"
)

// loginLimit is what stands between `POST /v1/login` and anybody who
// can reach it: two limits, for the two things an unauthenticated caller
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
// The backoff bounds the guessing at any one name: after a failure the
// name is refused with 429 for a window that doubles, one second, two,
// four, up to fifteen minutes, and a success clears it. A refusal inside
// the window costs no hash and does not lengthen it. It is a backoff
// rather than a lockout because a lockout is a way for anybody who knows
// the break-glass account's name to keep the operator out of it.
//
// Every name is held to it, whether an account by that name exists or
// not. Holding only the real ones would make the 429 an oracle for which
// names are real, which the login's single failure message exists to
// deny.
type loginLimit struct {
	checking chan struct{}

	mu    sync.Mutex
	names map[string]*loginBackoff
}

type loginBackoff struct {
	failures int
	until    time.Time
}

const (
	loginBackoffMax = 15 * time.Minute
	// loginBackoffNames bounds the table of names, which come from the
	// caller. When it is full, the entry whose window ends soonest goes:
	// a flood of new names, each one second into its window, is evicted
	// before a name somebody has been guessing at for fifteen minutes,
	// so the flood cannot be used to clear the name it is aimed at.
	loginBackoffNames = 4096
)

func newLoginLimit() *loginLimit {
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	return &loginLimit{checking: make(chan struct{}, n), names: map[string]*loginBackoff{}}
}

// wait reports how long name must wait before it may try again, or zero.
func (l *loginLimit) wait(name string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.names[name]
	if !ok || !now.Before(b.until) {
		return 0
	}
	return b.until.Sub(now)
}

// failed starts or doubles name's window.
func (l *loginLimit) failed(name string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.names[name]
	if !ok {
		if len(l.names) >= loginBackoffNames {
			l.evict(now)
		}
		b = &loginBackoff{}
		l.names[name] = b
	} else if !now.Before(b.until.Add(loginBackoffMax)) {
		// Quiet for a whole maximum window since the last one ended:
		// start again at one second, as a name never seen would.
		b.failures = 0
	}
	b.failures++
	window := time.Second << min(b.failures-1, 10)
	if window > loginBackoffMax {
		window = loginBackoffMax
	}
	b.until = now.Add(window)
}

// succeeded clears name's window.
func (l *loginLimit) succeeded(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.names, name)
}

// evict drops what has expired, and if nothing had, the entry whose
// window ends soonest. The caller holds the lock.
func (l *loginLimit) evict(now time.Time) {
	var soonest string
	var soonestAt time.Time
	for name, b := range l.names {
		if !now.Before(b.until.Add(loginBackoffMax)) {
			// Expired long enough ago that a failure now would start
			// again at one second anyway.
			delete(l.names, name)
			continue
		}
		if soonest == "" || b.until.Before(soonestAt) {
			soonest, soonestAt = name, b.until
		}
	}
	if len(l.names) >= loginBackoffNames && soonest != "" {
		delete(l.names, soonest)
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
