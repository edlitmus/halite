package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These check the parts that make a trace join up across a process
// boundary, which is the half `internal/tracing` shipped without: the
// carrier, the header, and the settings that turn it on.

// A span put into a context comes back out, and one that was never put
// there comes back as a working nil.
//
// The nil half is the load-bearing one. Every call site in the hub and
// the node reads its parent out of a context, and tracing being off
// must produce the same code path as tracing being on — not a second
// one that only runs on the estates where nobody turned it on.
func TestAContextCarriesASpanAndTheAbsenceOfOne(t *testing.T) {
	tr := &Tracer{Sampler: AlwaysSample{}, Export: &collect{}}
	tr.Start()
	defer tr.Stop(context.Background())

	span := tr.StartSpan(SpanContext{}, "job", KindServer)
	ctx := ContextWithSpan(context.Background(), span)
	if got := SpanFrom(ctx); got != span {
		t.Fatalf("the context gave back %v", got)
	}
	if parent := ParentFrom(ctx); parent != span.Context {
		t.Errorf("ParentFrom = %+v, want %+v", parent, span.Context)
	}

	if got := SpanFrom(context.Background()); got != nil {
		t.Errorf("an empty context produced a span: %v", got)
	}
	if parent := ParentFrom(context.Background()); parent.IsValid() {
		t.Error("an empty context produced a valid parent; a child of it would join a trace that does not exist")
	}

	// Storing a nil span must not make a context that behaves
	// differently from one that never had a span.
	empty := ContextWithSpan(context.Background(), nil)
	if got := SpanFrom(empty); got != nil {
		t.Errorf("storing a nil span produced %v", got)
	}
}

// The header goes on when there is a span and does not when there is
// not.
//
// A `traceparent` naming an all-zero trace is worse than no header: the
// receiver reads it as a parent that exists and hangs its span off
// nothing.
func TestTheHeaderIsWrittenOnlyWhenThereIsSomethingToPropagate(t *testing.T) {
	tr := &Tracer{Sampler: AlwaysSample{}, Export: &collect{}}
	tr.Start()
	defer tr.Stop(context.Background())
	span := tr.StartSpan(SpanContext{}, "job", KindServer)

	h := http.Header{}
	Inject(h, span)
	if got := h.Get(TraceParentHeader); got != FormatTraceParent(span.Context) {
		t.Errorf("traceparent = %q", got)
	}

	off := http.Header{}
	Inject(off, nil)
	if got := off.Get(TraceParentHeader); got != "" {
		t.Errorf("a nil span wrote %q", got)
	}

	// What Inject writes, Extract reads back unchanged. This is the
	// whole contract between two halite processes, so it is checked
	// rather than assumed.
	if back := Extract(h); back != span.Context {
		t.Errorf("extracted %+v, injected %+v", back, span.Context)
	}
}

// A header that does not parse means "no parent", not "fail".
//
// W3C says so, and the operational argument is stronger than the
// specification one: a proxy that rewrites the header badly must not
// stop a fleet applying its states.
func TestAMalformedHeaderStartsANewTraceRatherThanFailing(t *testing.T) {
	for _, bad := range []string{
		"",
		"garbage",
		"00-notahexvalue-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736",
	} {
		h := http.Header{}
		h.Set(TraceParentHeader, bad)
		if sc := Extract(h); sc.IsValid() {
			t.Errorf("%q was read as a valid parent: %+v", bad, sc)
		}
	}
	if sc := Extract(nil); sc.IsValid() {
		t.Error("a nil header produced a parent")
	}
}

// Stopping twice drains once and does not panic.
//
// Found by wiring this up rather than by writing it: a hub stops its
// tracer in a deferred call, and a test that also drained it explicitly
// -- so that it could read what had been exported -- took the process
// down on `close of closed channel`. Two shutdown paths meeting is the
// normal case for shutdown, and the second one arriving must not be
// worse than it never arriving.
func TestStoppingTwiceDrainsOnceAndDoesNotPanic(t *testing.T) {
	exported := &collect{}
	tr := &Tracer{Sampler: AlwaysSample{}, Export: exported}
	tr.Start()

	tr.StartSpan(SpanContext{}, "job", KindServer).End()

	tr.Stop(context.Background())
	// The second call is the one that used to panic. It must also still
	// be a barrier: a caller that stops and then reads has to see
	// everything the first stop drained.
	tr.Stop(context.Background())
	tr.Stop(context.Background())

	if got := exported.count(); got != 1 {
		t.Errorf("exported %d spans after stopping three times, want 1", got)
	}
	// And on a tracer that was never started, which is what tracing
	// being off looks like to a deferred flush.
	(&Tracer{}).Stop(context.Background())
	var nilTracer *Tracer
	nilTracer.Stop(context.Background())
	nilTracer.Stop(context.Background())
}

// The settings turn it on, turn it off, and refuse the ways of asking
// for nothing while believing you asked for something.
func TestTheSettingsBuildATracerOrSayWhyNot(t *testing.T) {
	for _, mode := range []string{"", "off", "OFF", " Off "} {
		tr, err := TracerFrom(Settings{Mode: mode})
		if err != nil {
			t.Errorf("mode %q: %v", mode, err)
		}
		if tr != nil {
			t.Errorf("mode %q produced a tracer", mode)
		}
	}

	tr, err := TracerFrom(Settings{Mode: "otlp", Service: "halite-hub", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if tr == nil {
		t.Fatal("otlp produced no tracer")
	}
	if tr.Service != "halite-hub" {
		t.Errorf("service = %q", tr.Service)
	}
	// An unset rate is the default rather than zero, because zero
	// records nothing and an operator who set nothing did not ask for
	// nothing.
	if s, ok := tr.Sampler.(RatioSampler); !ok || s.Ratio != DefaultSampleRate {
		t.Errorf("sampler = %#v, want the default ratio", tr.Sampler)
	}
	if e, ok := tr.Export.(*OTLPExporter); !ok || e.Endpoint != DefaultEndpoint {
		t.Errorf("exporter = %#v, want the default endpoint", tr.Export)
	}

	for _, tc := range []struct {
		name string
		in   Settings
		says string
	}{
		{"a mode that is not one of the two", Settings{Mode: "jaeger"}, "off"},
		{"an explicit zero rate", Settings{Mode: "otlp", SampleRate: 0, SampleRateSet: true}, "records nothing"},
		{"a rate above one", Settings{Mode: "otlp", SampleRate: 2, SampleRateSet: true}, "between 0 and 1"},
		{"a negative rate", Settings{Mode: "otlp", SampleRate: -1, SampleRateSet: true}, "between 0 and 1"},
		{"an endpoint that is not a URL", Settings{Mode: "otlp", Endpoint: "collector:4318"}, "not a URL"},
		{"an endpoint that is not HTTP", Settings{Mode: "otlp", Endpoint: "grpc://c:4317"}, "http or https"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, err := TracerFrom(tc.in)
			if err == nil {
				t.Fatalf("accepted, and built %#v", built)
			}
			if built != nil {
				t.Errorf("refused and still produced a tracer")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error does not say %q: %v", tc.says, err)
			}
		})
	}
}

// An explicit rate of 1 is accepted, because a lab wants one.
//
// Named separately from the refusals because it is one character away
// from the zero that is refused, and a reader of that table would
// otherwise reasonably wonder.
func TestAnExplicitRateOfOneIsAccepted(t *testing.T) {
	tr, err := TracerFrom(Settings{Mode: "otlp", SampleRate: 1, SampleRateSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := tr.Sampler.(RatioSampler); !ok || s.Ratio != 1 {
		t.Errorf("sampler = %#v", tr.Sampler)
	}
}

// The two halves meet: a server reads the parent a client wrote, and the
// child span is in the same trace.
//
// This is the property the whole file exists for. Checked over a real
// HTTP request rather than by handing a header to a function, because
// what goes wrong here goes wrong in the transport — a header dropped by
// a round tripper, or a request cloned after the header was set.
func TestATraceSurvivesAnHTTPBoundary(t *testing.T) {
	tr := &Tracer{Sampler: AlwaysSample{}, Export: &collect{}}
	tr.Start()
	defer tr.Stop(context.Background())

	served := make(chan *Span, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := tr.StartSpan(Extract(r.Header), "file serve", KindServer)
		s.End()
		served <- s
	}))
	defer srv.Close()

	client := tr.StartSpan(SpanContext{}, "file fetch", KindClient)
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	Inject(req.Header, client)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	client.End()

	hub := <-served
	if hub == nil {
		t.Fatal("the server started no span")
	}
	if hub.Context.TraceID != client.Context.TraceID {
		t.Errorf("the server's span is in trace %s and the client's in %s; the trace is two traces",
			hub.Context.TraceID, client.Context.TraceID)
	}
	if hub.Parent != client.Context.SpanID {
		t.Errorf("the server's span has parent %s, want %s", hub.Parent, client.Context.SpanID)
	}
	if !hub.Context.Sampled {
		t.Error("the server's span is not sampled and the client's was; the decision was re-made")
	}
}
