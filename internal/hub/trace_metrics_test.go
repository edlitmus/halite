package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/tracing"
)

// refusingCollector is a collector that is down: every batch fails.
type refusingCollector struct{}

func (refusingCollector) ExportSpans(context.Context, string, string, []*tracing.Span) error {
	return errors.New("connection refused")
}

// acceptingCollector takes every batch.
type acceptingCollector struct{}

func (acceptingCollector) ExportSpans(context.Context, string, string, []*tracing.Span) error {
	return nil
}

// SPEC 26.2's counter for the tracer's drop paths, read off /v1/metrics
// as an operator reads it (issue 609).
//
// Three hubs: one not tracing, one whose collector takes every batch, and
// one whose collector is down. All three export both reasons -- the first
// two at 0, which is what lets increase() see the third hub's first loss
// -- and only the third moves, by exactly the spans it lost. A healthy
// exporter that moved the counter would page somebody for nothing; a
// failing one that did not is the silence the issue was about.
func TestLostSpansAreExposedAndOnlyAFailingExporterMovesThem(t *testing.T) {
	for _, tc := range []struct {
		name         string
		export       tracing.Exporter // nil: tracing off
		failed, full string
	}{
		{name: "tracing off", failed: "0", full: "0"},
		{name: "healthy collector", export: acceptingCollector{}, failed: "0", full: "0"},
		{name: "collector down", export: refusingCollector{}, failed: "3", full: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := metricsLab(t, metricsPolicy)
			if tc.export != nil {
				l.server.Tracer = &tracing.Tracer{
					Service: "halite-hub", Sampler: tracing.AlwaysSample{}, Export: tc.export,
					BatchSize: 1, BatchWait: time.Hour,
				}
				l.server.Tracer.Start()
				for i := 0; i < 3; i++ {
					l.server.Tracer.StartSpan(tracing.SpanContext{}, "dispatch test.ping", tracing.KindServer).End()
				}
				// Stop drains the queue and waits for the exporter, so
				// every batch has been tried before the scrape.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				l.server.Tracer.Stop(ctx)
			}

			out, err := scrape(t, l, "ed")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"# TYPE halite_trace_spans_dropped_total counter",
				`halite_trace_spans_dropped_total{reason="export_failed"} ` + tc.failed + "\n",
				`halite_trace_spans_dropped_total{reason="queue_full"} ` + tc.full + "\n",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("the hub's exposition has no %q:\n%s", strings.TrimSpace(want), grepFamily(out, "halite_trace_"))
				}
			}
		})
	}
}

// grepFamily keeps the lines naming a prefix, so a failure shows the
// family rather than three hundred lines of exposition.
func grepFamily(out, prefix string) string {
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, prefix) {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}
