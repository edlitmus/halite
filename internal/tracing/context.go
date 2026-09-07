// Package tracing is SPEC 26.3: W3C Trace Context propagation, a span
// per job, per state and per file transfer, and OTLP over HTTP with JSON
// encoding.
//
// # No SDK
//
// SPEC chooses OTLP/HTTP with JSON deliberately — "a documented
// protobuf-equivalent JSON schema over HTTP" that "needs no OpenTelemetry
// SDK". That is a dependency decision as much as a wire one: the Go
// OpenTelemetry SDK is a large tree with its own release cadence, and
// SPEC 4.2 makes every dependency an argument somebody has to win. What
// it costs is that this package owns two external formats rather than
// importing them, so both are written down here against their
// specifications and checked against the documents' own examples.
//
// # Off by default, and free when off
//
// A build with tracing off does no work: no goroutine, no buffer, no
// allocation per job. `Tracer` is a nil pointer in that case and every
// method on it returns immediately, which is why they are written to
// tolerate one.
package tracing

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// TraceID is a 16-byte identifier shared by every span of one trace.
type TraceID [16]byte

// SpanID is an 8-byte identifier for one span.
type SpanID [8]byte

func (t TraceID) String() string { return hex.EncodeToString(t[:]) }
func (s SpanID) String() string  { return hex.EncodeToString(s[:]) }

// IsZero reports the all-zero value, which W3C Trace Context makes
// invalid rather than merely empty: a traceparent carrying one is
// malformed, and a span cannot be started from it.
func (t TraceID) IsZero() bool { return t == TraceID{} }
func (s SpanID) IsZero() bool  { return s == SpanID{} }

// SpanContext is what travels between processes: which trace, which
// span, and whether it is being recorded.
type SpanContext struct {
	TraceID TraceID
	SpanID  SpanID
	// Sampled is the trace's own decision, made once at the root and
	// carried from there.
	//
	// It is not re-decided downstream, and that is the whole point of
	// propagating it. A hub that sampled a job in and a node that
	// sampled it out produce a trace with a hole in the middle, which
	// is worse than either sampling everything or nothing: the span
	// that is missing is the one somebody is looking for.
	Sampled bool
}

// IsValid reports whether this context can parent a span.
func (sc SpanContext) IsValid() bool { return !sc.TraceID.IsZero() && !sc.SpanID.IsZero() }

// TraceParentHeader is the W3C Trace Context header name.
//
// Lower case because the specification writes it that way and HTTP
// header names are case-insensitive; the wire message field uses the
// same spelling so there is one name to search for.
const TraceParentHeader = "traceparent"

// TraceStateHeader carries vendor-specific state alongside traceparent.
//
// This build propagates it unchanged and never writes to it. W3C
// requires a system that does not understand tracestate to pass it
// through rather than drop it, because dropping it silently breaks
// whatever put it there — and halite has nothing to add to it.
const TraceStateHeader = "tracestate"

// version00 is the only traceparent version this build writes.
//
// The specification says a receiver must accept a higher version whose
// first four fields it understands, so ParseTraceParent does, and it
// says a writer must write the version it implements, so this build
// writes 00.
const version00 = "00"

// ParseTraceParent reads a `traceparent` header.
//
//	00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
//	^  ^                                ^                ^
//	|  trace-id, 16 bytes               parent-id        flags
//	version
//
// A malformed header is not an error a caller has to handle: W3C says a
// receiver that cannot parse one must behave as though it were absent
// and start a new trace, rather than refusing the request. So this
// returns a zero context and `false`, and every caller treats that the
// same way it treats no header at all — a job with an unreadable
// traceparent is still a job.
//
// The all-zero trace-id and span-id are invalid per the specification
// and are refused here, which matters: a caller that accepted them would
// export spans belonging to a trace nothing can join.
func ParseTraceParent(header string) (SpanContext, bool) {
	header = strings.TrimSpace(header)
	parts := strings.Split(header, "-")
	if len(parts) < 4 {
		return SpanContext{}, false
	}
	// A future version may add fields; the first four keep their
	// meaning, which is what the specification's forward-compatibility
	// rule promises. Version ff is reserved and invalid.
	if len(parts[0]) != 2 || parts[0] == "ff" {
		return SpanContext{}, false
	}
	if parts[0] == version00 && len(parts) != 4 {
		// Version 00 is exactly four fields. Accepting a longer one
		// here would accept a header no version defines.
		return SpanContext{}, false
	}
	if len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return SpanContext{}, false
	}

	var sc SpanContext
	traceID, err := hex.DecodeString(parts[1])
	if err != nil {
		return SpanContext{}, false
	}
	spanID, err := hex.DecodeString(parts[2])
	if err != nil {
		return SpanContext{}, false
	}
	flags, err := hex.DecodeString(parts[3])
	if err != nil {
		return SpanContext{}, false
	}
	copy(sc.TraceID[:], traceID)
	copy(sc.SpanID[:], spanID)
	if !sc.IsValid() {
		return SpanContext{}, false
	}
	// Bit 0 is `sampled`. The other seven are reserved and this build
	// neither reads nor writes them.
	sc.Sampled = flags[0]&0x01 == 0x01
	return sc, true
}

// FormatTraceParent renders a context as the header.
func FormatTraceParent(sc SpanContext) string {
	flags := "00"
	if sc.Sampled {
		flags = "01"
	}
	return fmt.Sprintf("%s-%s-%s-%s", version00, sc.TraceID, sc.SpanID, flags)
}

// NewTraceID and NewSpanID produce identifiers.
//
// crypto/rand rather than math/rand: SPEC 4.2's dependency policy has a
// build check against math/rand for exactly this reason, and an
// identifier that another process might guess is one that another
// process might collide with.
func NewTraceID() (TraceID, error) {
	var id TraceID
	for {
		if _, err := rand.Read(id[:]); err != nil {
			return TraceID{}, err
		}
		// The all-zero value is invalid, and a generator that could
		// return it would produce a trace nothing can join. Astronomically
		// unlikely and cheap to exclude.
		if !id.IsZero() {
			return id, nil
		}
	}
}

func NewSpanID() (SpanID, error) {
	var id SpanID
	for {
		if _, err := rand.Read(id[:]); err != nil {
			return SpanID{}, err
		}
		if !id.IsZero() {
			return id, nil
		}
	}
}
