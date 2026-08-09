// Package returner sends finished job results somewhere durable. The event
// bus is deliberately in-memory and lossy; returners are the answer to
// "what happened on web1 last Tuesday".
//
// Results are handed to returners through a queue, so a slow or broken sink
// delays its own writes and nothing else. A full queue drops records and
// says so, on the same principle as the bus: a sink must never be able to
// stall the control plane or an agent's return.
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
	// Return writes one record. It may block; the manager calls it from a
	// single goroutine and nothing waits on it.
	Return(Record) error
	// Close releases whatever the returner holds.
	Close() error
}

// queueDepth is how many records may be waiting before new ones are
// dropped. A fleet-wide highstate return burst fits comfortably.
const queueDepth = 512

// Manager fans records out to every configured returner.
type Manager struct {
	returners []Returner
	queue     chan Record
	log       *log.Logger

	mu      sync.Mutex
	dropped int
}

// NewManager builds a manager over the given returners. It does nothing
// until Run is called.
func NewManager(returners []Returner, logger *log.Logger) *Manager {
	return &Manager{
		returners: returners,
		queue:     make(chan Record, queueDepth),
		log:       logger,
	}
}

// Configured reports whether there is anything to send to.
func (m *Manager) Configured() bool { return m != nil && len(m.returners) > 0 }

// Submit queues a record. It never blocks: if the queue is full the record
// is dropped and counted.
func (m *Manager) Submit(rec Record) {
	if !m.Configured() {
		return
	}
	select {
	case m.queue <- rec:
	default:
		m.mu.Lock()
		m.dropped++
		dropped := m.dropped
		m.mu.Unlock()
		m.log.Printf("returner queue full, dropped a result (%d total)", dropped)
	}
}

// Run drains the queue until done is closed, then closes every returner.
// It is meant to be called in its own goroutine.
func (m *Manager) Run(done <-chan struct{}) {
	if !m.Configured() {
		return
	}
	for {
		select {
		case rec := <-m.queue:
			m.deliver(rec)
		case <-done:
			// Flush whatever is already queued before shutting down, so a
			// clean stop does not lose results that were accepted.
			for {
				select {
				case rec := <-m.queue:
					m.deliver(rec)
				default:
					m.closeAll()
					return
				}
			}
		}
	}
}

func (m *Manager) deliver(rec Record) {
	for _, r := range m.returners {
		if err := r.Return(rec); err != nil {
			m.log.Printf("returner %s: %v", r.Name(), err)
		}
	}
}

func (m *Manager) closeAll() {
	for _, r := range m.returners {
		if err := r.Close(); err != nil {
			m.log.Printf("returner %s: closing: %v", r.Name(), err)
		}
	}
}

// Dropped reports how many records were lost to a full queue.
func (m *Manager) Dropped() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropped
}

// Parse builds a returner from a "kind:target" specification:
//
//	file:/var/log/halite/results.ndjson
//	webhook:https://example.com/halite
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
