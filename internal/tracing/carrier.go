package tracing

import (
	"context"
	"net/http"
)

// This file is how a span reaches code that was not written to take one.
//
// A trace is only useful if the span a piece of work belongs to is
// available where that work happens, and most of what halite does is
// reached through interfaces that predate tracing and take no span: a
// module calls `c.Files.Fetch`, a client posts a return. Threading a
// `*Span` through every one of those signatures would be a large change
// to seams that have nothing to do with telemetry, and every one of
// them is a place a future caller forgets.
//
// So the span travels in the `context.Context` that is already there,
// and crosses a process boundary in the `traceparent` header that W3C
// defines for exactly this. Both directions are nil-safe: a context
// with no span yields an invalid SpanContext, and starting a span from
// an invalid parent starts a root.

type spanKey struct{}

// ContextWithSpan returns a context carrying s.
//
// A nil span is stored as nothing rather than as a nil value, so that
// `SpanFrom` on the result behaves the same as on a context that never
// had one — tracing being off must not produce a second code path.
func ContextWithSpan(ctx context.Context, s *Span) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, spanKey{}, s)
}

// SpanFrom returns the span a context carries, or nil.
//
// Nil is a working span — every method tolerates it — so a caller does
// not check.
func SpanFrom(ctx context.Context) *Span {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(spanKey{}).(*Span)
	return s
}

// ParentFrom returns the span context to start a child under.
//
// It is what a call site passes to StartSpan: the context's span when
// there is one, and the zero SpanContext otherwise, which starts a new
// trace.
func ParentFrom(ctx context.Context) SpanContext {
	if s := SpanFrom(ctx); s != nil {
		return s.Context
	}
	return SpanContext{}
}

// Inject writes the span's identity into outgoing headers.
//
// Called on every request halite makes to another halite process, and
// on a request whose span is nil it writes nothing — a header naming an
// all-zero trace is worse than no header, because a receiver reads it as
// a parent that exists.
func Inject(h http.Header, s *Span) {
	if h == nil || s == nil || !s.Context.IsValid() {
		return
	}
	h.Set(TraceParentHeader, FormatTraceParent(s.Context))
}

// InjectContext is Inject for the span a context carries.
func InjectContext(h http.Header, ctx context.Context) {
	Inject(h, SpanFrom(ctx))
}

// Extract reads an incoming request's parent.
//
// A header that does not parse is treated as absent rather than as an
// error: a malformed `traceparent` from a proxy must not fail a job. W3C
// says the same — an unparseable header means start a new trace.
func Extract(h http.Header) SpanContext {
	if h == nil {
		return SpanContext{}
	}
	sc, ok := ParseTraceParent(h.Get(TraceParentHeader))
	if !ok {
		return SpanContext{}
	}
	return sc
}
