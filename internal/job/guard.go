package job

import (
	"container/list"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Refusal is why a node would not run a job. SPEC 6.3 requires that a
// refusal be structured and returned, not dropped: an operator watching
// a job that vanished learns nothing, and a replay that is silently
// ignored looks the same as a network fault.
type Refusal struct {
	Reason string
	Detail string
}

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return r.Reason
	}
	return r.Reason + ": " + r.Detail
}

// The reasons, as stable tokens.
const (
	ReasonReplayed = "replayed"
	ReasonExpired  = "expired"
	ReasonMalforme = "malformed"
)

// ErrRefused matches any refusal, for a caller that only wants to know
// that the job did not run.
var ErrRefused = errors.New("the job was refused")

func (r *Refusal) Is(target error) bool { return target == ErrRefused }

// Guard is the node's bounded replay cache of SPEC 6.3.
//
// Bounded, because a cache that grows with every job is a node that
// runs out of memory after a long enough uptime, and unbounded caches
// are how that happens quietly. The oldest entry is evicted; a replay
// older than the window is possible in theory and is bounded in
// practice by the job's own expiry, which is the shorter of the two.
type Guard struct {
	mu    sync.Mutex
	size  int
	seen  map[string]*list.Element
	order *list.List
	// Now is the clock, for the tests.
	Now func() time.Time
}

// DefaultGuardSize holds roughly a day of a busy node's jobs.
const DefaultGuardSize = 4096

// NewGuard makes a replay cache.
func NewGuard(size int) *Guard {
	if size <= 0 {
		size = DefaultGuardSize
	}
	return &Guard{size: size, seen: make(map[string]*list.Element, size), order: list.New()}
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Admit records a job and reports whether it may run.
//
// It refuses a job whose jid it has already executed, whose nonce it
// has already seen, or whose expiry has passed -- the three checks SPEC
// 6.3 names, in that order, so the message says the most useful thing
// first.
func (g *Guard) Admit(j *Job) error {
	if j == nil || j.JID == "" {
		return &Refusal{Reason: ReasonMalforme, Detail: "the job carries no identifier"}
	}
	if !j.JID.Valid() {
		return &Refusal{Reason: ReasonMalforme, Detail: fmt.Sprintf("%q is not a job identifier", j.JID)}
	}
	if j.Nonce == "" {
		return &Refusal{Reason: ReasonMalforme, Detail: "the job carries no nonce, so a replay of it could not be detected"}
	}
	now := g.now()
	if j.Expired(now) {
		return &Refusal{
			Reason: ReasonExpired,
			Detail: fmt.Sprintf("it expired at %s and it is now %s",
				j.Expires.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)),
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	for _, key := range []string{"jid:" + string(j.JID), "nonce:" + j.Nonce} {
		if _, ok := g.seen[key]; ok {
			return &Refusal{
				Reason: ReasonReplayed,
				Detail: fmt.Sprintf("this node has already run %s", j.JID),
			}
		}
	}
	g.record("jid:" + string(j.JID))
	g.record("nonce:" + j.Nonce)
	return nil
}

// record adds a key and evicts the oldest if the cache is full. Called
// with the lock held.
func (g *Guard) record(key string) {
	g.seen[key] = g.order.PushBack(key)
	for g.order.Len() > g.size*2 {
		oldest := g.order.Front()
		if oldest == nil {
			return
		}
		g.order.Remove(oldest)
		delete(g.seen, oldest.Value.(string))
	}
}

// Len is how many keys are held, for a test and for a metric.
func (g *Guard) Len() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.seen)
}
