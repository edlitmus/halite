// Package returner sends finished job results somewhere durable. The event
// bus is deliberately in-memory and lossy; returners are the answer to
// "what happened on web1 last Tuesday".
//
// Each returner gets its own queue and its own delivery goroutine, so a
// slow or broken sink delays its own writes and nothing else. A full queue
// drops records and says so, on the same principle as the bus: a sink must
// never be able to stall the control plane or an agent's return.
package returner

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// Record is one agent's answer to one job, with the job it answered.
type Record struct {
	Time   time.Time           `json:"time"`
	Job    transport.Job       `json:"job"`
	Result transport.JobResult `json:"result"`
}

// Returner writes records somewhere. Implementations are configured by an
// operator and run inside the control plane.
type Returner interface {
	// Name identifies the returner in logs.
	Name() string
	// Return writes one record. It may block; the manager calls it from
	// this returner's own delivery goroutine and nothing waits on it.
	Return(Record) error
	// Close releases whatever the returner holds.
	Close() error
}

// queueDepth is how many records may be waiting for one returner before new
// ones are dropped. A fleet-wide highstate return burst fits comfortably.
const queueDepth = 512

// sink is one returner with its own queue, so it can only ever starve
// itself.
type sink struct {
	r       Returner
	queue   chan Record
	dropped int // guarded by Manager.mu
}

// Manager fans records out to every configured returner.
type Manager struct {
	sinks []*sink
	log   *log.Logger

	mu     sync.Mutex
	closed bool
}

// NewManager builds a manager over the given returners. It does nothing
// until Run is called.
func NewManager(returners []Returner, logger *log.Logger) *Manager {
	m := &Manager{log: logger}
	for _, r := range returners {
		m.sinks = append(m.sinks, &sink{r: r, queue: make(chan Record, queueDepth)})
	}
	return m
}

// Configured reports whether there is anything to send to.
func (m *Manager) Configured() bool { return m != nil && len(m.sinks) > 0 }

// Submit queues a record for every returner. It never blocks: if a
// returner's queue is full the record is dropped for that returner and
// counted; the others still get their copy. After Run has begun shutting
// down, everything is dropped — a closed manager must refuse a record
// rather than strand it in a channel nothing drains.
func (m *Manager) Submit(rec Record) {
	if !m.Configured() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sinks {
		if m.closed {
			s.dropped++
			m.log.Printf("returner %s stopped, dropped a result (%d total)", s.r.Name(), s.dropped)
			continue
		}
		select {
		case s.queue <- rec:
		default:
			s.dropped++
			m.log.Printf("returner %s queue full, dropped a result (%d total)", s.r.Name(), s.dropped)
		}
	}
}

// Run delivers queued records, one goroutine per returner, until done is
// closed; then it flushes whatever each returner had accepted, closes every
// returner, and returns. It is meant to be called in its own goroutine, and
// its return is the signal that every accepted record is flushed — a caller
// that wants a clean shutdown waits for it.
func (m *Manager) Run(done <-chan struct{}) {
	if !m.Configured() {
		return
	}
	var wg sync.WaitGroup
	for _, s := range m.sinks {
		wg.Add(1)
		go func(s *sink) {
			defer wg.Done()
			// A closed channel hands over its backlog before the range
			// ends, so this loop is both the steady state and the flush.
			for rec := range s.queue {
				if err := s.r.Return(rec); err != nil {
					m.log.Printf("returner %s: %v", s.r.Name(), err)
				}
			}
			if err := s.r.Close(); err != nil {
				m.log.Printf("returner %s: closing: %v", s.r.Name(), err)
			}
		}(s)
	}
	<-done
	// Stop the intake before closing the queues: Submit checks closed under
	// the same lock, so no record can be sent to a closed channel and none
	// can be stranded behind the flush.
	m.mu.Lock()
	m.closed = true
	for _, s := range m.sinks {
		close(s.queue)
	}
	m.mu.Unlock()
	wg.Wait()
}

// Dropped reports how many records were lost to a full queue or to
// shutdown, summed across returners.
func (m *Manager) Dropped() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, s := range m.sinks {
		total += s.dropped
	}
	return total
}

// Parse builds a returner from a "kind:target" specification:
//
//	file:/var/log/halite/results.ndjson
//	webhook:https://example.com/halite
//
// A webhook endpoint must be https unless it is on the loopback.
func Parse(spec string) (Returner, error) {
	kind, target, found := strings.Cut(spec, ":")
	if !found || target == "" {
		return nil, fmt.Errorf("returner %q is not kind:target (file:PATH or webhook:URL)", spec)
	}
	switch kind {
	case "file":
		return NewFile(target)
	case "webhook":
		return NewWebhook(target)
	default:
		return nil, fmt.Errorf("unknown returner kind %q (file, webhook)", kind)
	}
}

// encode renders a record as one line of JSON.
func encode(rec Record) ([]byte, error) {
	line, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("encode result: %w", err)
	}
	return append(line, '\n'), nil
}
