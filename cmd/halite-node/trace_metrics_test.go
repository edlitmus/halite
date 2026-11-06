package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/config"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/tracing"
)

// SPEC 26.2's counter for the tracer's drop paths, scraped from the
// node's own /v1/metrics (issue 609).
//
// Built the way the agent builds it -- buildTracer from the node's
// configuration, with the real OTLP exporter -- and pointed at a
// collector that takes every batch, at one that answers 503 to every
// batch, and at none (tracing off). Every case exports both reasons;
// only the collector that refuses moves export_failed, and by the spans
// it refused. The wiring is the thing under test as much as the count:
// the family is declared by buildTracer, so a node that built its tracer
// without declaring it would fail every case here.
func TestANodeExposesLostSpansAndOnlyAFailingCollectorMovesThem(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tracing bool
		status  int
		failed  string
	}{
		{name: "tracing off", failed: "0"},
		{name: "healthy collector", tracing: true, status: http.StatusOK, failed: "0"},
		{name: "collector refuses", tracing: true, status: http.StatusServiceUnavailable, failed: "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ca := newTestCA(t)
			dir := t.TempDir()
			certFile, keyFile := ca.serving(t, dir, "node")
			body := "id: web1.example\nmetrics_listen: '127.0.0.1:0'\n" +
				"metrics_tls_cert: " + certFile + "\nmetrics_tls_key: " + keyFile + "\n"
			if tc.tracing {
				collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(tc.status)
				}))
				defer collector.Close()
				body += "tracing: otlp\ntracing_endpoint: " + collector.URL + "\ntracing_sample_rate: 1\n"
			}
			cfg, err := config.Load(config.Node, config.LoadOptions{Path: writeConfig(t, dir, body)})
			if err != nil {
				t.Fatal(err)
			}
			logger, err := hlog.New(hlog.Options{Level: hlog.Error, Format: hlog.JSON, Stderr: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			n := &node{cfg: cfg, log: logger, metrics: newNodeMetrics(cfg)}
			n.buildTracer()
			if (n.tracer != nil) != tc.tracing {
				t.Fatalf("tracer built = %v, want %v", n.tracer != nil, tc.tracing)
			}
			if n.tracer != nil {
				n.tracer.BatchSize = 1
				n.tracer.Start()
				for i := 0; i < 3; i++ {
					n.tracer.StartSpan(tracing.SpanContext{}, "job state.apply", tracing.KindServer).End()
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				n.tracer.Stop(ctx)
			}

			addr := serveForTest(t, n.metrics)
			res, err := ca.client(t, nil).Get("https://" + addr + "/v1/metrics")
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			raw, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			out := string(raw)
			checkExposition(t, out)
			for _, want := range []string{
				"# TYPE halite_trace_spans_dropped_total counter",
				`halite_trace_spans_dropped_total{reason="export_failed"} ` + tc.failed + "\n",
				`halite_trace_spans_dropped_total{reason="queue_full"} 0` + "\n",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("the node's exposition has no %q:\n%s", strings.TrimSpace(want), out)
				}
			}
		})
	}
}
