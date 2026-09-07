package tracing

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Tracer starts spans and hands the finished ones to an exporter.
//
// **A nil Tracer is the off switch.** SPEC 26.3 says tracing is off by
// default, and off here means a nil pointer whose methods return
// immediately: no goroutine, no buffer, and no span allocated per job.
// Every method tolerates a nil receiver so that a caller does not guard,
// because a caller that has to guard eventually forgets to.
type Tracer struct {
	// Service is what this process calls itself in the exported
	// resource: halite-hub, halite-node, halite-api.
	Service string
	// Version is the build identity, so a span can be attributed to the
	// binary that produced it across an upgrade.
	Version string

	// Sampler decides whether a *root* trace is recorded. A span with a
	// parent inherits the parent's decision and never consults this.
	Sampler Sampler

	// Export receives batches of finished spans. Called from the
	// tracer's own goroutine, never from a caller's.
	Export Exporter

	// BatchSize and BatchWait bound a batch. A batch goes when it is
	// full or when it is old, whichever first, so a quiet hub still
	// exports rather than holding one span until the next.
	BatchSize int
	BatchWait time.Duration

	// QueueDepth bounds what is waiting to be exported. Past it, spans
	// are dropped and counted — see finished.
	QueueDepth int

	start   sync.Once
	halt    sync.Once
	queue   chan *Span
	stop    chan struct{}
	stopped chan struct{}
	dropped atomic.Int64
}

// Exporter sends a batch somewhere.
type Exporter interface {
	// ExportSpans must not retain the slice. It is called from one
	// goroutine at a time and may block; a slow exporter costs dropped
	// spans rather than a stalled hub, which is the trade `finished`
	// makes explicit.
	ExportSpans(ctx context.Context, service, version string, spans []*Span) error
}

// Sampler decides whether to record a new trace.
type Sampler interface {
	// SampleRoot is consulted only for a span with no parent. Its
	// argument is the new trace's identifier, so a sampler can be
	// deterministic on it — the same trace sampled the same way
	// wherever the decision is made.
	SampleRoot(id TraceID) bool
}

// Defaults, which an operator overrides through configuration.
const (
	DefaultBatchSize  = 256
	DefaultBatchWait  = 5 * time.Second
	DefaultQueueDepth = 2048
)

func (t *Tracer) batchSize() int {
	if t.BatchSize > 0 {
		return t.BatchSize
	}
	return DefaultBatchSize
}

func (t *Tracer) batchWait() time.Duration {
	if t.BatchWait > 0 {
		return t.BatchWait
	}
	return DefaultBatchWait
}

func (t *Tracer) queueDepth() int {
	if t.QueueDepth > 0 {
		return t.QueueDepth
	}
	return DefaultQueueDepth
}

// Start begins the exporting goroutine. Safe to call more than once and
// on a nil Tracer.
func (t *Tracer) Start() {
	if t == nil {
		return
	}
	t.start.Do(func() {
		t.queue = make(chan *Span, t.queueDepth())
		t.stop = make(chan struct{})
		t.stopped = make(chan struct{})
		go t.run()
	})
}

// Stop drains what is queued and waits for the exporter.
//
// It waits, rather than abandoning the queue, because the spans most
// worth having are usually the last ones before something stopped. A
// caller that cannot wait passes a context with a deadline.
//
// Stopping twice is not an error. Shutdown is where two paths meet --
// a deferred flush and a signal handler, a test's cleanup and its own
// explicit drain -- and a shutdown path that panics when both run is a
// process that dies noisily while doing the right thing. The second call
// still waits for the drain the first started, so a caller that stops
// and then reads what was exported sees all of it.
func (t *Tracer) Stop(ctx context.Context) {
	if t == nil || t.stop == nil {
		return
	}
	t.halt.Do(func() { close(t.stop) })
	select {
	case <-t.stopped:
	case <-ctx.Done():
	}
}

// Dropped is how many spans were discarded because the queue was full.
//
// Reported rather than logged per span: a hub under enough load to drop
// spans would produce a log line per drop, which costs more than the
// tracing did. The hub turns this into a counter.
func (t *Tracer) Dropped() int64 {
	if t == nil {
		return 0
	}
	return t.dropped.Load()
}

// StartSpan begins a span, using parent when it is valid.
//
// The parent's sampling decision is inherited rather than re-made. See
// SpanContext.Sampled: a trace sampled in at the hub and out at the node
// has a hole exactly where somebody is looking.
//
// An unsampled span is still returned and still propagates — it carries
// the trace and span identifiers so that the next process can continue
// the trace — but it records nothing and is never exported. That is what
// W3C asks for: a system that is not recording must still pass the
// context along, or a sampled trace that crosses it loses its middle.
func (t *Tracer) StartSpan(parent SpanContext, name string, kind Kind) *Span {
	if t == nil {
		return nil
	}
	spanID, err := NewSpanID()
	if err != nil {
		// Without an identifier there is no span. Returning nil rather
		// than a broken one keeps every caller's `defer span.End()`
		// correct.
		return nil
	}

	sc := SpanContext{SpanID: spanID}
	var parentID SpanID
	if parent.IsValid() {
		sc.TraceID = parent.TraceID
		sc.Sampled = parent.Sampled
		parentID = parent.SpanID
	} else {
		traceID, err := NewTraceID()
		if err != nil {
			return nil
		}
		sc.TraceID = traceID
		sc.Sampled = t.sample(traceID)
	}

	s := &Span{
		Context: sc,
		Parent:  parentID,
		Name:    name,
		Kind:    kind,
		Start:   time.Now(),
	}
	if sc.Sampled {
		s.tracer = t
	}
	return s
}

func (t *Tracer) sample(id TraceID) bool {
	if t.Sampler == nil {
		return false
	}
	return t.Sampler.SampleRoot(id)
}

// finished queues a span, and drops it rather than waiting.
//
// This is the decision that keeps tracing from becoming an outage. The
// caller is a hub dispatching a job or a node applying a state, and
// neither may be made slower by an exporter that is behind — a
// telemetry pipeline is allowed to lose data and a fleet is not allowed
// to stop. The drop is counted so that the loss is visible, on the same
// argument the reactor's queue overflow makes.
func (t *Tracer) finished(s *Span) {
	if t == nil || t.queue == nil {
		return
	}
	select {
	case t.queue <- s:
	default:
		t.dropped.Add(1)
	}
}

// run batches finished spans and exports them.
func (t *Tracer) run() {
	defer close(t.stopped)
	ticker := time.NewTicker(t.batchWait())
	defer ticker.Stop()

	batch := make([]*Span, 0, t.batchSize())
	flush := func() {
		if len(batch) == 0 {
			return
		}
		// A deadline of its own: an export that hung would hold the
		// queue and turn dropped spans into every span.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = t.Export.ExportSpans(ctx, t.Service, t.Version, batch)
		cancel()
		batch = batch[:0]
	}

	for {
		select {
		case s := <-t.queue:
			batch = append(batch, s)
			if len(batch) >= t.batchSize() {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-t.stop:
			// Drain what is queued before going. The spans nobody has
			// seen yet are the ones from just before whatever stopped
			// this process.
			for {
				select {
				case s := <-t.queue:
					batch = append(batch, s)
					if len(batch) >= t.batchSize() {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		}
	}
}

// RatioSampler records a fixed share of traces.
//
// The decision is made from the trace identifier rather than from a
// counter or a random draw, so that two processes given the same trace
// would reach the same answer. Nothing in this build needs that today —
// the decision is made once at the root and propagated — but a sampler
// that disagreed with itself across processes is the kind of thing that
// is discovered from a trace with a hole in it, and determinism costs
// nothing here.
type RatioSampler struct {
	// Ratio is between 0 and 1. Zero records nothing, which is what an
	// operator who turned tracing on and left the ratio unset would
	// otherwise get by accident — so `TracerFromConfig` refuses that
	// combination rather than exporting silence.
	Ratio float64
}

func (r RatioSampler) SampleRoot(id TraceID) bool {
	switch {
	case r.Ratio <= 0:
		return false
	case r.Ratio >= 1:
		return true
	}
	// The last eight bytes as a big-endian unsigned integer, compared
	// against the ratio. The W3C specification says the trace id must
	// be random enough that any subset of bits can be used this way.
	var n uint64
	for _, b := range id[8:] {
		n = n<<8 | uint64(b)
	}
	const max = float64(1 << 63)
	return float64(n>>1) < r.Ratio*max
}

// AlwaysSample records every trace, for a lab and for a test.
type AlwaysSample struct{}

func (AlwaysSample) SampleRoot(TraceID) bool { return true }

// NeverSample records none, and is what an unconfigured tracer does.
type NeverSample struct{}

func (NeverSample) SampleRoot(TraceID) bool { return false }
