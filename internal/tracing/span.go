package tracing

import (
	"sync"
	"time"
)

// Kind is a span's role, as OTLP numbers them.
//
// Only the three this build produces are named. OTLP has five; the two
// left out are the messaging ones, and naming a constant nothing sets
// would be an invitation to set it wrongly.
type Kind int

const (
	// KindInternal is work inside one process: a state compiling, a
	// tree rendering.
	KindInternal Kind = 1
	// KindServer is work done because something asked: the hub
	// handling a submission, a node running a job it was given.
	KindServer Kind = 2
	// KindClient is work this process asked another to do: a node
	// fetching a file, the hub dispatching to a node.
	KindClient Kind = 3
)

// StatusCode is a span's outcome, as OTLP numbers them.
type StatusCode int

const (
	// StatusUnset is the default and means nobody said. It is not the
	// same as Ok: OTLP reserves Ok for a caller that explicitly decided
	// the operation succeeded, so that a span left unset can be
	// interpreted by whatever is reading rather than claiming a success
	// nothing asserted.
	StatusUnset StatusCode = 0
	StatusOk    StatusCode = 1
	StatusError StatusCode = 2
)

// Span is one unit of work.
//
// Fields are written by the goroutine that started it and read by the
// exporter after End, which is the only ordering this needs; the mutex
// guards the attribute map, because a state run adds attributes from the
// same span while a requisite fans out.
type Span struct {
	Context SpanContext
	// Parent is empty for a root span.
	Parent SpanID
	Name   string
	Kind   Kind

	Start time.Time
	// End is zero until the span is finished. The exporter never sees a
	// span with a zero End: `End()` is what hands it over.
	Finish time.Time

	Status  StatusCode
	Message string

	mu    sync.Mutex
	attrs map[string]any

	// tracer is where this span goes when it ends. Nil for a span
	// nobody is recording, which is what makes an unsampled span free.
	tracer *Tracer
	once   sync.Once
}

// SetAttr records one attribute.
//
// A nil span accepts and ignores it, which is what makes the call sites
// clean: `span.SetAttr(...)` needs no guard because tracing being off
// produces a nil span rather than a special one.
func (s *Span) SetAttr(key string, value any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = make(map[string]any, 4)
	}
	s.attrs[key] = value
}

// SetStatus records the outcome.
//
// An error's message is kept because OTLP carries one and because "this
// span failed" without saying why moves the question to a log line
// somebody then has to correlate — which is the thing tracing exists to
// stop.
func (s *Span) SetStatus(code StatusCode, message string) {
	if s == nil {
		return
	}
	s.Status = code
	s.Message = message
}

// Fail is the common case: an error ends a span.
func (s *Span) Fail(err error) {
	if s == nil || err == nil {
		return
	}
	s.SetStatus(StatusError, err.Error())
}

// End finishes the span and hands it to the exporter.
//
// Idempotent, because a span is often ended by a `defer` and again on an
// error path, and a span exported twice is a duplicate in whatever is
// reading. Ending one twice is a mistake worth tolerating rather than
// one worth crashing over: it changes nothing that anybody sees.
func (s *Span) End() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.Finish = time.Now()
		if s.tracer != nil {
			s.tracer.finished(s)
		}
	})
}

// Attributes returns a copy, for the exporter.
func (s *Span) Attributes() map[string]any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attrs) == 0 {
		return nil
	}
	out := make(map[string]any, len(s.attrs))
	for k, v := range s.attrs {
		out[k] = v
	}
	return out
}

// SpanContextOf returns a span's context, tolerating a nil span.
//
// Callers propagate with this rather than reaching into the field, so
// that a process with tracing off puts no header on the wire instead of
// an all-zero one — which a receiver would refuse anyway, and which
// would look like a bug in whoever sent it.
func SpanContextOf(s *Span) (SpanContext, bool) {
	if s == nil || !s.Context.IsValid() {
		return SpanContext{}, false
	}
	return s.Context, true
}
