package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/edlitmus/halite/internal/tracing"
)

// The `traceparent` header goes on every request this client makes, and
// only when there is a trace to propagate.
//
// Asserted at the round tripper rather than at each request builder,
// because that is where it is applied. There are ten builders and there
// will be more; a header that has to be remembered at each one is a
// header that is on nine of them, and a file fetch traced end to end
// beside a pillar compile that is not is worse than neither — the gap
// reads as the hub declining to take part.
func TestEveryRequestCarriesTheTraceAndOnlyWhenThereIsOne(t *testing.T) {
	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get(tracing.TraceParentHeader)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := &http.Client{Transport: tracePropagating{next: http.DefaultTransport}}

	tr := &tracing.Tracer{Sampler: tracing.AlwaysSample{}, Export: discardSpans{}}
	tr.Start()
	defer tr.Stop(context.Background())
	span := tr.StartSpan(tracing.SpanContext{}, "file fetch", tracing.KindClient)

	// With a span in the request's context.
	req, err := http.NewRequestWithContext(
		tracing.ContextWithSpan(context.Background(), span), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got, want := <-seen, tracing.FormatTraceParent(span.Context); got != want {
		t.Errorf("traceparent = %q, want %q", got, want)
	}

	// And without one, which is what every request on an estate with
	// tracing off looks like.
	plain, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = client.Do(plain)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := <-seen; got != "" {
		t.Errorf("a request with no span carried traceparent %q", got)
	}
}

// The round tripper does not modify the request it is handed.
//
// net/http may retry a request, and a RoundTripper that mutates its
// argument is the kind of thing that works until the day a connection is
// reused and it does not.
func TestTheRoundTripperLeavesTheRequestItWasGivenAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	tr := &tracing.Tracer{Sampler: tracing.AlwaysSample{}, Export: discardSpans{}}
	tr.Start()
	defer tr.Stop(context.Background())
	span := tr.StartSpan(tracing.SpanContext{}, "job", tracing.KindClient)

	req, err := http.NewRequestWithContext(
		tracing.ContextWithSpan(context.Background(), span), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&http.Client{Transport: tracePropagating{next: http.DefaultTransport}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if got := req.Header.Get(tracing.TraceParentHeader); got != "" {
		t.Errorf("the caller's request was modified: traceparent = %q", got)
	}
}

type discardSpans struct{}

func (discardSpans) ExportSpans(context.Context, string, string, []*tracing.Span) error { return nil }
