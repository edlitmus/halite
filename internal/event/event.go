// Package event is halite's event bus: an in-process publish/subscribe
// broker that the control plane uses to announce what is happening, and
// that the reactor, returners, and `halite events` consume.
//
// The bus never blocks its publisher. A subscriber that stops reading has
// its buffer fill and then loses events, with the loss counted and
// reported — a slow consumer must not be able to stall the control plane.
package event

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Tags follow Salt's slash-delimited convention, most general first.
const (
	TagAgentHello    = "halite/agent/%s/hello"
	TagAgentEnrolled = "halite/agent/%s/enrolled"
	TagKeyPending    = "halite/key/%s/pending"
	TagKeyAccepted   = "halite/key/%s/accepted"
	TagKeyRejected   = "halite/key/%s/rejected"
	TagJobDispatch   = "halite/job/%s/dispatch"
	TagJobReturn     = "halite/job/%s/ret/%s"
	TagBeacon        = "halite/beacon/%s/%s"
)

// Event is one thing that happened.
type Event struct {
	ID     string         `json:"id"`
	Tag    string         `json:"tag"`
	Time   time.Time      `json:"time"`
	Source string         `json:"source"` // agent id, or "master"
	Data   map[string]any `json:"data,omitempty"`
}

// SourceMaster is the source of events the control plane raises itself.
const SourceMaster = "master"

// defaultBuffer is how many events a subscriber may fall behind by before
// it starts losing them. Deep enough to absorb a burst of returns from a
// fleet-wide job, shallow enough that a dead consumer is noticed.
const defaultBuffer = 256

// defaultHistory is how many recent events the bus keeps for subscribers
// that connect after the fact.
const defaultHistory = 1000

// Bus is the broker. The zero value is not usable; call NewBus.
type Bus struct {
	mu          sync.Mutex
	subscribers map[int]*subscription
	nextHandle  int
	history     []Event
	maxHistory  int
	buffer      int
}

type subscription struct {
	pattern string
	ch      chan Event
	dropped int
}

// NewBus returns a bus with the default buffer and history sizes.
func NewBus() *Bus {
	return &Bus{
		subscribers: map[int]*subscription{},
		maxHistory:  defaultHistory,
		buffer:      defaultBuffer,
	}
}

// Publish delivers an event to every matching subscriber and records it in
// history. It never blocks: a full subscriber buffer means a dropped event,
// not a stalled publisher.
func (b *Bus) Publish(ev Event) {
	if ev.ID == "" {
		ev.ID = newID()
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	if ev.Source == "" {
		ev.Source = SourceMaster
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.history = append(b.history, ev)
	if len(b.history) > b.maxHistory {
		b.history = b.history[len(b.history)-b.maxHistory:]
	}
	for _, sub := range b.subscribers {
		if !TagMatch(sub.pattern, ev.Tag) {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			sub.dropped++
		}
	}
}

// Emit is the common case: publish a tagged event with data.
func (b *Bus) Emit(tag, source string, data map[string]any) {
	b.Publish(Event{Tag: tag, Source: source, Data: data})
}

// Subscribe returns a channel of events matching pattern, plus a function
// that unsubscribes and closes the channel. An empty pattern matches
// everything. The caller must call cancel, and must keep reading.
func (b *Bus) Subscribe(pattern string) (<-chan Event, func()) {
	if pattern == "" {
		pattern = "**"
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	handle := b.nextHandle
	b.nextHandle++
	sub := &subscription{pattern: pattern, ch: make(chan Event, b.buffer)}
	b.subscribers[handle] = sub

	return sub.ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if existing, ok := b.subscribers[handle]; ok {
			delete(b.subscribers, handle)
			close(existing.ch)
		}
	}
}

// History returns up to limit recent events matching pattern, oldest
// first. It lets a subscriber that has just connected see what it missed.
func (b *Bus) History(pattern string, limit int) []Event {
	if pattern == "" {
		pattern = "**"
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []Event
	for _, ev := range b.history {
		if TagMatch(pattern, ev.Tag) {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Dropped reports how many events each subscriber has lost, summed. It is
// the signal that a consumer is too slow, not a diagnostic to act on
// per-subscriber.
func (b *Bus) Dropped() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := 0
	for _, sub := range b.subscribers {
		total += sub.dropped
	}
	return total
}

// TagMatch reports whether a tag matches a pattern. Tags are slash
// delimited: `*` matches within one segment and `**` matches any number of
// segments, so `halite/job/*/ret/*` matches one job's returns while
// `halite/job/**` matches everything about jobs.
func TagMatch(pattern, tag string) bool {
	return segmentsMatch(strings.Split(pattern, "/"), strings.Split(tag, "/"))
}

func segmentsMatch(pattern, tag []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			// Try to consume zero or more tag segments.
			if len(pattern) == 1 {
				return true
			}
			for i := 0; i <= len(tag); i++ {
				if segmentsMatch(pattern[1:], tag[i:]) {
					return true
				}
			}
			return false
		}
		if len(tag) == 0 {
			return false
		}
		if !globMatch(pattern[0], tag[0]) {
			return false
		}
		pattern, tag = pattern[1:], tag[1:]
	}
	return len(tag) == 0
}

// globMatch matches one segment, where `*` stands for any run of
// characters within that segment.
func globMatch(pattern, segment string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == segment
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(segment, parts[0]) {
		return false
	}
	segment = segment[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		index := strings.Index(segment, parts[i])
		if index < 0 {
			return false
		}
		segment = segment[index+len(parts[i]):]
	}
	last := parts[len(parts)-1]
	return strings.HasSuffix(segment, last) && len(segment) >= len(last)
}

// newID is time-ordered so events sort chronologically, with a random tail
// so two in the same microsecond stay distinct.
func newID() string {
	stamp := time.Now().UTC().Format("20060102150405.000000")
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return stamp
	}
	return stamp + "-" + hex.EncodeToString(suffix[:])
}
