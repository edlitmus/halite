package tracing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

type failingExporter struct{}

func (failingExporter) ExportSpans(context.Context, string, string, []*Span) error {
	return errors.New("the collector is down")
}

// A batch the exporter did not take is counted and reported, and does not
// disappear: `Dropped` counted only a full queue, so a collector that was
// down showed no loss anywhere. DIVERGENCE 5.548.
func TestAFailedExportIsCountedAndReported(t *testing.T) {
	var (
		mu    sync.Mutex
		seen  []string
		spans int
	)
	tr := &Tracer{
		Service: "x", Sampler: AlwaysSample{}, Export: failingExporter{},
		BatchSize: 2, BatchWait: time.Hour,
		OnExportError: func(err error, n int) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, err.Error())
			spans += n
		},
	}
	tr.Start()
	for i := 0; i < 4; i++ {
		tr.StartSpan(SpanContext{}, "job", KindServer).End()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tr.Stop(ctx)

	if tr.Failed() != 4 {
		t.Errorf("Failed = %d, want the 4 spans in the two batches the exporter refused", tr.Failed())
	}
	if tr.Dropped() != 0 {
		t.Errorf("Dropped = %d: nothing was lost to the queue", tr.Dropped())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || spans != 4 || !strings.Contains(seen[0], "collector is down") {
		t.Errorf("OnExportError saw %v for %d spans, want 2 batches of 2", seen, spans)
	}

	noExporter := &Tracer{Sampler: AlwaysSample{}, BatchSize: 1, BatchWait: time.Hour}
	noExporter.Start()
	noExporter.StartSpan(SpanContext{}, "job", KindServer).End()
	noExporter.Stop(ctx)
	if noExporter.Failed() != 1 {
		t.Errorf("a tracer with no exporter lost %d spans without counting them, want 1", noExporter.Failed())
	}
}

// One attribute that JSON cannot hold does not cost the batch it is in.
func TestANaNAttributeDoesNotFailTheBatch(t *testing.T) {
	s := &Span{Name: "x", Start: time.Now(), Finish: time.Now()}
	s.SetAttr("ratio", math.NaN())
	s.SetAttr("limit", math.Inf(1))
	s.SetAttr("ok", 1.5)
	if _, err := json.Marshal(BuildPayload("svc", "v", []*Span{s})); err != nil {
		t.Fatalf("a NaN attribute failed the whole payload: %v", err)
	}
}

func TestExportErrorsAreLoggedOncePerMinute(t *testing.T) {
	var lines []map[string]any
	hook := WarnOnExportError(func(msg string, kv ...any) {
		m := map[string]any{"msg": msg}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		lines = append(lines, m)
	})
	for i := 0; i < 5; i++ {
		hook(errors.New("down"), 10)
	}
	if len(lines) != 1 || lines[0]["spans"] != 10 {
		t.Errorf("five failures in a row logged %v, want one line for the first", lines)
	}
}
