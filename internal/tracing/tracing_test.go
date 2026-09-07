package tracing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// This package owns two external formats rather than importing them,
// which SPEC 26.3 chose deliberately — OTLP/HTTP with JSON "needs no
// OpenTelemetry SDK". The cost of that choice is that nothing but these
// tests stands between a mistake in either format and a collector
// quietly misreading every span.
//
// So both are checked against their own specifications' examples rather
// than against what this build happens to produce. That is the lesson
// `pf` cost: a fixture written in the module's own spelling proves the
// module reads itself.

// The W3C Trace Context specification's own example parses.
//
// From the specification's section 3.2.1, which gives this exact header
// and names each field's value. A test written from the code would agree
// with whatever the code did; this one agrees with the document.
func TestTheSpecificationsOwnTraceParentParses(t *testing.T) {
	const header = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

	sc, ok := ParseTraceParent(header)
	if !ok {
		t.Fatal("the specification's own example did not parse")
	}
	if got := sc.TraceID.String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id read as %q", got)
	}
	if got := sc.SpanID.String(); got != "00f067aa0ba902b7" {
		t.Errorf("span id read as %q", got)
	}
	if !sc.Sampled {
		t.Error("flags 01 read as not sampled")
	}
	// And it round-trips, so what this build puts on the wire is what it
	// would accept back.
	if got := FormatTraceParent(sc); got != header {
		t.Errorf("round trip produced %q, want %q", got, header)
	}
}

// A traceparent this build cannot read is treated as absent.
//
// W3C requires a receiver that cannot parse one to start a new trace
// rather than refuse the request: a malformed header from something
// upstream must not stop a job. So every one of these returns "no
// context" and no caller distinguishes it from no header at all.
func TestAnUnreadableTraceParentIsTreatedAsAbsent(t *testing.T) {
	for _, tc := range []struct{ what, header string }{
		{"empty", ""},
		{"not a traceparent at all", "hello"},
		{"too few fields", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7"},
		{"version 00 with a fifth field",
			"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra"},
		{"the reserved version ff",
			"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{"a short trace id", "00-4bf92f3577b34da6-00f067aa0ba902b7-01"},
		{"a short span id", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa-01"},
		{"not hex", "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01"},
		{"an all-zero trace id, which the specification makes invalid",
			"00-00000000000000000000000000000000-00f067aa0ba902b7-01"},
		{"an all-zero span id, likewise",
			"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"},
	} {
		if _, ok := ParseTraceParent(tc.header); ok {
			t.Errorf("%s was accepted: %q", tc.what, tc.header)
		}
	}
}

// A version this build does not know is still read, if its first four
// fields are there.
//
// The specification's forward-compatibility rule: a receiver must accept
// a higher version whose first four fields it understands, because the
// alternative is that every trace stops crossing this process the day
// somebody upstream upgrades.
func TestAHigherTraceParentVersionIsStillRead(t *testing.T) {
	sc, ok := ParseTraceParent(
		"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-what-comes-next")
	if !ok {
		t.Fatal("a future version was refused, so a trace stops crossing this process " +
			"the day anything upstream upgrades")
	}
	if sc.TraceID.String() != "4bf92f3577b34da6a3ce929d0e0e4736" || !sc.Sampled {
		t.Errorf("a future version parsed to %+v", sc)
	}
}

// The two things OTLP/JSON does that proto3 JSON does not.
//
// Both produce a body a collector accepts and misreads, which is the
// worst kind of wrong: base64 identifiers are valid strings and a
// float64 timestamp is a valid number, so nothing anywhere errors and
// the traces are silently useless.
func TestTheOTLPPayloadUsesHexIdsAndStringTimes(t *testing.T) {
	span := &Span{
		Context: SpanContext{
			TraceID: mustTraceID(t, "4bf92f3577b34da6a3ce929d0e0e4736"),
			SpanID:  mustSpanID(t, "00f067aa0ba902b7"),
			Sampled: true,
		},
		Parent: mustSpanID(t, "0102030405060708"),
		Name:   "job",
		Kind:   KindServer,
		// A nanosecond value with digits a float64 cannot hold: 2^53 is
		// about 9.007e15 and a nanosecond timestamp is about 1.7e18, so
		// the last two digits are exactly what a JSON number loses.
		Start:  time.Unix(0, 1757000000123456789),
		Finish: time.Unix(0, 1757000000987654321),
		Status: StatusError,
	}
	span.Message = "the node refused"
	span.SetAttr("halite.jid", "20260907T012233445566")
	span.SetAttr("halite.nodes", 12)

	raw, err := json.Marshal(BuildPayload("halite-hub", "1.0.0", []*Span{span}))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	// Hex, not base64. The base64 of that trace id would be
	// "S/kvNXezTaajzpKdDg5HNg==", which is a perfectly good string that
	// a collector would file under a trace nothing can join.
	if !strings.Contains(body, `"traceId":"4bf92f3577b34da6a3ce929d0e0e4736"`) {
		t.Errorf("the trace id is not lower-case hex:\n%s", body)
	}
	if !strings.Contains(body, `"spanId":"00f067aa0ba902b7"`) {
		t.Errorf("the span id is not lower-case hex:\n%s", body)
	}
	if !strings.Contains(body, `"parentSpanId":"0102030405060708"`) {
		t.Errorf("the parent span id is not lower-case hex:\n%s", body)
	}

	// Strings, not numbers, and the exact digits.
	if !strings.Contains(body, `"startTimeUnixNano":"1757000000123456789"`) {
		t.Errorf("the start time is not an exact string:\n%s", body)
	}
	if !strings.Contains(body, `"endTimeUnixNano":"1757000000987654321"`) {
		t.Errorf("the end time is not an exact string:\n%s", body)
	}
	// An int attribute is a string too, for the same reason.
	if !strings.Contains(body, `"intValue":"12"`) {
		t.Errorf("an integer attribute is not a string:\n%s", body)
	}

	// And the resource carries what a collector groups by.
	if !strings.Contains(body, `"service.name"`) || !strings.Contains(body, "halite-hub") {
		t.Errorf("the payload has no service.name, so a collector files it under unknown:\n%s", body)
	}
	if !strings.Contains(body, `"code":2`) || !strings.Contains(body, "the node refused") {
		t.Errorf("the status did not survive:\n%s", body)
	}
}

// A root span omits parentSpanId rather than sending a zero one.
//
// A present-but-zero parent is read by a collector as a parent that does
// not exist, and the span is hung off nothing instead of being a root.
func TestARootSpanOmitsItsParent(t *testing.T) {
	span := &Span{
		Context: SpanContext{TraceID: mustTraceID(t, "4bf92f3577b34da6a3ce929d0e0e4736"),
			SpanID: mustSpanID(t, "00f067aa0ba902b7")},
		Name:   "root",
		Start:  time.Unix(0, 1),
		Finish: time.Unix(0, 2),
	}
	raw, err := json.Marshal(BuildPayload("halite-node", "1.0.0", []*Span{span}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "parentSpanId") {
		t.Errorf("a root span sent a parent:\n%s", raw)
	}
}

// A nil tracer is the off switch, and costs nothing.
//
// SPEC 26.3 has tracing off by default. Off here is a nil pointer, and
// every method tolerates one so that no call site needs a guard — a call
// site that has to guard eventually forgets to, and the forgotten one is
// a nil dereference in a hub.
func TestANilTracerIsTheOffSwitch(t *testing.T) {
	var tr *Tracer
	tr.Start()
	tr.Stop(context.Background())
	if got := tr.Dropped(); got != 0 {
		t.Errorf("a nil tracer dropped %d", got)
	}

	span := tr.StartSpan(SpanContext{}, "job", KindServer)
	if span != nil {
		t.Fatal("a nil tracer produced a span")
	}
	// And every method on that nil span is safe, which is what makes the
	// call sites clean.
	span.SetAttr("k", "v")
	span.SetStatus(StatusOk, "")
	span.Fail(errors.New("boom"))
	span.End()
	span.End()
	if attrs := span.Attributes(); attrs != nil {
		t.Errorf("a nil span has attributes: %v", attrs)
	}
	if _, ok := SpanContextOf(span); ok {
		t.Error("a nil span offered a context to propagate; an all-zero traceparent " +
			"would go on the wire and be refused at the other end")
	}
}

// A sampling decision is inherited, never re-made.
//
// A trace sampled in at the hub and out at the node has a hole exactly
// where somebody is looking. The parent's bit wins, whatever this
// process's own sampler would have said.
func TestTheSamplingDecisionIsInheritedAndNotRemade(t *testing.T) {
	never := &Tracer{Sampler: NeverSample{}, Export: &collect{}}
	never.Start()
	defer never.Stop(context.Background())

	parent := SpanContext{
		TraceID: mustTraceID(t, "4bf92f3577b34da6a3ce929d0e0e4736"),
		SpanID:  mustSpanID(t, "00f067aa0ba902b7"),
		Sampled: true,
	}
	child := never.StartSpan(parent, "state", KindInternal)
	if !child.Context.Sampled {
		t.Error("a process that samples nothing dropped a trace something upstream sampled")
	}
	if child.Context.TraceID != parent.TraceID {
		t.Error("the child is in a different trace from its parent")
	}
	if child.Parent != parent.SpanID {
		t.Error("the child does not name its parent")
	}

	// And the reverse: a process that samples everything does not
	// promote a trace something upstream declined.
	always := &Tracer{Sampler: AlwaysSample{}, Export: &collect{}}
	always.Start()
	defer always.Stop(context.Background())
	unsampled := always.StartSpan(SpanContext{
		TraceID: parent.TraceID, SpanID: parent.SpanID, Sampled: false,
	}, "state", KindInternal)
	if unsampled.Context.Sampled {
		t.Error("a process that samples everything recorded a trace something upstream declined")
	}
}

// An unsampled span still propagates and is never exported.
//
// W3C asks a system that is not recording to pass the context along
// anyway, or a sampled trace crossing it loses its middle. So the span
// exists, carries identifiers, and goes nowhere.
func TestAnUnsampledSpanPropagatesAndExportsNothing(t *testing.T) {
	sink := &collect{}
	tr := &Tracer{Sampler: NeverSample{}, Export: sink, BatchWait: 10 * time.Millisecond}
	tr.Start()

	span := tr.StartSpan(SpanContext{}, "job", KindServer)
	if span == nil {
		t.Fatal("no span at all; there is nothing to propagate")
	}
	sc, ok := SpanContextOf(span)
	if !ok || !sc.IsValid() {
		t.Fatal("an unsampled span has no context to propagate")
	}
	if sc.Sampled {
		t.Error("it claims to be sampled")
	}
	span.End()

	tr.Stop(context.Background())
	if n := sink.count(); n != 0 {
		t.Errorf("%d unsampled spans were exported", n)
	}
}

// A finished span reaches the exporter, and a full queue drops rather
// than blocks.
//
// The drop is the decision that keeps tracing from becoming an outage:
// the caller is a hub dispatching a job, and it may not be made slower
// by a collector that is behind. The loss is counted so it is visible.
func TestAFullQueueDropsRatherThanBlocks(t *testing.T) {
	sink := &collect{block: make(chan struct{})}
	tr := &Tracer{
		Sampler: AlwaysSample{}, Export: sink,
		QueueDepth: 2, BatchSize: 1, BatchWait: time.Millisecond,
	}
	tr.Start()

	// More spans than the queue can hold, with the exporter wedged.
	const many = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < many; i++ {
			tr.StartSpan(SpanContext{}, "job", KindServer).End()
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ending spans blocked on a wedged exporter; a slow collector would " +
			"have become a slow hub")
	}
	if tr.Dropped() == 0 {
		t.Error("nothing was dropped and nothing blocked, so this test established nothing")
	}
	t.Logf("%d of %d spans dropped with the exporter wedged", tr.Dropped(), many)

	close(sink.block)
	tr.Stop(context.Background())
}

// Stopping drains what is queued.
//
// The spans nobody has seen yet are the ones from just before whatever
// stopped this process, which is when somebody wants them most.
func TestStoppingDrainsTheQueue(t *testing.T) {
	sink := &collect{}
	tr := &Tracer{Sampler: AlwaysSample{}, Export: sink, BatchWait: time.Hour}
	tr.Start()
	for i := 0; i < 5; i++ {
		tr.StartSpan(SpanContext{}, "job", KindServer).End()
	}
	// BatchWait is an hour, so nothing has been exported on a timer.
	tr.Stop(context.Background())
	if n := sink.count(); n != 5 {
		t.Errorf("%d of 5 spans survived a stop", n)
	}
}

// Ending a span twice exports it once.
//
// A span is often ended by a `defer` and again on an error path, and a
// duplicate is a duplicate in whatever is reading.
func TestEndingASpanTwiceExportsItOnce(t *testing.T) {
	sink := &collect{}
	tr := &Tracer{Sampler: AlwaysSample{}, Export: sink, BatchWait: time.Hour}
	tr.Start()
	span := tr.StartSpan(SpanContext{}, "job", KindServer)
	span.End()
	span.End()
	span.End()
	tr.Stop(context.Background())
	if n := sink.count(); n != 1 {
		t.Errorf("a span ended three times was exported %d times", n)
	}
}

// The exporter posts to the collector's traces path, whichever way the
// endpoint was written.
func TestTheExporterFindsTheTracesPath(t *testing.T) {
	for _, tc := range []struct{ what, endpoint string }{
		{"a bare base", ""},
		{"a base with a trailing slash", "/"},
		{"the full path already", TracesPath},
	} {
		var gotPath, gotType string
		var body []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotType = r.Header.Get("Content-Type")
			body, _ = readAll(r)
			w.WriteHeader(http.StatusOK)
		}))
		e := &OTLPExporter{Endpoint: srv.URL + tc.endpoint, Client: srv.Client()}
		err := e.ExportSpans(context.Background(), "halite-hub", "1.0.0", []*Span{finished()})
		srv.Close()
		if err != nil {
			t.Errorf("%s: %v", tc.what, err)
			continue
		}
		if gotPath != TracesPath {
			t.Errorf("%s: posted to %q, want %q", tc.what, gotPath, TracesPath)
		}
		if gotType != "application/json" {
			t.Errorf("%s: content type %q", tc.what, gotType)
		}
		if !strings.Contains(string(body), `"resourceSpans"`) {
			t.Errorf("%s: the body is not an OTLP payload: %s", tc.what, body)
		}
	}
}

// A collector that refuses is an error the caller can read.
func TestACollectorThatRefusesIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	e := &OTLPExporter{Endpoint: srv.URL, Client: srv.Client()}
	err := e.ExportSpans(context.Background(), "halite-hub", "1.0.0", []*Span{finished()})
	if err == nil {
		t.Fatal("a 401 was reported as a successful export")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the error does not say what happened: %v", err)
	}

	// And no endpoint at all says so rather than posting nowhere.
	none := &OTLPExporter{}
	if err := none.ExportSpans(context.Background(), "s", "v", []*Span{finished()}); err == nil {
		t.Error("an exporter with no endpoint reported success")
	}
}

// The ratio sampler is deterministic on the trace id.
//
// Two processes handed the same trace would reach the same answer.
// Nothing needs that today — the decision is propagated — but a sampler
// that disagreed with itself across processes is discovered from a trace
// with a hole in it.
func TestTheRatioSamplerIsDeterministicAndProportionate(t *testing.T) {
	id := mustTraceID(t, "4bf92f3577b34da6a3ce929d0e0e4736")
	s := RatioSampler{Ratio: 0.5}
	first := s.SampleRoot(id)
	for i := 0; i < 100; i++ {
		if s.SampleRoot(id) != first {
			t.Fatal("the same trace was sampled differently twice")
		}
	}

	// Zero records nothing and one records everything, which are the two
	// an operator sets on purpose.
	if (RatioSampler{Ratio: 0}).SampleRoot(id) {
		t.Error("a ratio of zero recorded a trace")
	}
	if !(RatioSampler{Ratio: 1}).SampleRoot(id) {
		t.Error("a ratio of one declined a trace")
	}

	// And a tenth is roughly a tenth. Loose bounds: this is a check that
	// the arithmetic is not inverted or off by an order of magnitude,
	// not a statistical test.
	tenth := RatioSampler{Ratio: 0.1}
	sampled := 0
	const runs = 4000
	for i := 0; i < runs; i++ {
		gen, err := NewTraceID()
		if err != nil {
			t.Fatal(err)
		}
		if tenth.SampleRoot(gen) {
			sampled++
		}
	}
	if sampled < runs/40 || sampled > runs/4 {
		t.Errorf("a ratio of 0.1 sampled %d of %d", sampled, runs)
	}
}

// --- helpers ---

type collect struct {
	mu    sync.Mutex
	spans []*Span
	block chan struct{}
}

func (c *collect) ExportSpans(_ context.Context, _, _ string, spans []*Span) error {
	if c.block != nil {
		<-c.block
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spans = append(c.spans, spans...)
	return nil
}

func (c *collect) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.spans)
}

func finished() *Span {
	return &Span{
		Context: SpanContext{TraceID: TraceID{1}, SpanID: SpanID{1}, Sampled: true},
		Name:    "job",
		Start:   time.Unix(0, 1),
		Finish:  time.Unix(0, 2),
	}
}

func mustTraceID(t *testing.T, hex string) TraceID {
	t.Helper()
	sc, ok := ParseTraceParent("00-" + hex + "-00f067aa0ba902b7-01")
	if !ok {
		t.Fatalf("%q is not a trace id", hex)
	}
	return sc.TraceID
}

func mustSpanID(t *testing.T, hex string) SpanID {
	t.Helper()
	sc, ok := ParseTraceParent("00-4bf92f3577b34da6a3ce929d0e0e4736-" + hex + "-01")
	if !ok {
		t.Fatalf("%q is not a span id", hex)
	}
	return sc.SpanID
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}
