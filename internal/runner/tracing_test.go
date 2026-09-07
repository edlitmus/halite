package runner

import (
	"context"
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/tracing"
)

// SPEC 26.3's span per state, checked against what actually ran.
//
// The interesting property is not that spans appear — it is *which*
// ones. A highstate is mostly declarations that converge or are held by
// a requisite, and a trace with a span for every chunk buries the four
// that did work among the four hundred that did not. So the span is
// started where the state is executed rather than in the loop that
// walks the chunks, and this is what holds it there.
func TestASpanIsRecordedForEachStateThatRanAndNoOthers(t *testing.T) {
	exported, tracer := recordingTracer(t)

	out, p := compileAndRun(t, `
broken:
  probe.run:
    - fail: true

skipped:
  probe.run:
    - require:
      - probe: broken

held:
  probe.run:
    - unless: true

second:
  probe.run: []
`, withTracer(tracer))
	tracer.Stop(context.Background())

	// What ran, according to the probe, is the ground truth here: the
	// spans are checked against it rather than against a list written
	// out by hand, which would agree with whatever the runner did.
	if len(p.ran) == 0 {
		t.Fatal("nothing ran")
	}
	spans := exported.names()
	if len(spans) != len(p.ran) {
		t.Errorf("%d states ran (%v) and %d spans were recorded (%v)",
			len(p.ran), p.ran, len(spans), spans)
	}
	for _, name := range spans {
		if name != "state probe.run" {
			t.Errorf("span %q is not named for the state function that ran", name)
		}
	}

	// The skipped ones are skipped, so this test is asserting an
	// absence that exists.
	if r := resultFor(out, "skipped"); !r.Skipped {
		t.Fatal("the requisite-skipped state ran; this test proves nothing")
	}
	if r := resultFor(out, "held"); !r.Skipped {
		t.Fatal("the unless-held state ran; this test proves nothing")
	}
}

// A span says whether its state changed anything, and fails when the
// state failed.
//
// "Which states actually did something" is the question a highstate
// trace is opened to answer. Without it every span in a converged run
// looks the same, and a converged run is nearly every run.
func TestAStateSpanSaysWhetherItChangedAnythingAndWhetherItFailed(t *testing.T) {
	exported, tracer := recordingTracer(t)

	compileAndRun(t, `
changed:
  probe.run:
    - changes: true

converged:
  probe.run: []

broken:
  probe.run:
    - fail: true
`, withTracer(tracer))
	tracer.Stop(context.Background())

	var changed, converged, failed int
	for _, s := range exported.all() {
		attrs := s.Attributes()
		switch attrs["halite.state.id"] {
		case "changed":
			changed++
			if attrs["halite.state.changed"] != true {
				t.Errorf("the state that changed something reports %v", attrs["halite.state.changed"])
			}
			if s.Status != tracing.StatusOk {
				t.Errorf("status = %v", s.Status)
			}
		case "converged":
			converged++
			if attrs["halite.state.changed"] != false {
				t.Errorf("the state that changed nothing reports %v", attrs["halite.state.changed"])
			}
		case "broken":
			failed++
			if s.Status != tracing.StatusError {
				t.Errorf("a failed state's span has status %v", s.Status)
			}
			if s.Message == "" {
				t.Error("a failed state's span carries no message")
			}
		default:
			t.Errorf("a span names state %v", attrs["halite.state.id"])
		}
	}
	if changed != 1 || converged != 1 || failed != 1 {
		t.Errorf("changed=%d converged=%d failed=%d, want one of each", changed, converged, failed)
	}
}

// Every state span hangs off the job's, rather than starting its own
// trace.
//
// This is what makes a highstate one trace instead of four hundred. The
// parent arrives through the module context, which is the only channel
// between the node's job span and this package.
func TestEveryStateSpanIsAChildOfWhateverStartedTheRun(t *testing.T) {
	exported, tracer := recordingTracer(t)
	jobSpan := tracer.StartSpan(tracing.SpanContext{}, "job state.apply", tracing.KindServer)

	compileAndRun(t, `
one:
  probe.run: []

two:
  probe.run: []
`, withTracer(tracer), func(r *Runner) {
		r.Ctx.Ctx = tracing.ContextWithSpan(context.Background(), jobSpan)
	})
	jobSpan.End()
	tracer.Stop(context.Background())

	states := 0
	for _, s := range exported.all() {
		if s.Name == "job state.apply" {
			continue
		}
		states++
		if s.Context.TraceID != jobSpan.Context.TraceID {
			t.Errorf("%s is in trace %s and the job is in %s",
				s.Name, s.Context.TraceID, jobSpan.Context.TraceID)
		}
		if s.Parent != jobSpan.Context.SpanID {
			t.Errorf("%s has parent %s, want the job's %s", s.Name, s.Parent, jobSpan.Context.SpanID)
		}
	}
	if states != 2 {
		t.Errorf("recorded %d state spans, want 2", states)
	}
}

// A runner with no tracer runs the same states and records nothing.
//
// Tracing off is the default and is what nearly every run is, so it is
// the case that has to be checked rather than assumed: a nil tracer is
// dereferenced on every state, and once would be enough.
func TestARunnerWithNoTracerStillRuns(t *testing.T) {
	out, p := compileAndRun(t, `
one:
  probe.run:
    - changes: true

two:
  probe.run:
    - fail: true
`)
	if len(p.ran) != 2 {
		t.Errorf("ran %v", p.ran)
	}
	if !resultFor(out, "two").Result.Failed() {
		t.Error("the failing state did not fail")
	}
}

func withTracer(tr *tracing.Tracer) func(*Runner) {
	return func(r *Runner) { r.Tracer = tr }
}

func recordingTracer(t *testing.T) (*spanSink, *tracing.Tracer) {
	t.Helper()
	sink := &spanSink{}
	tr := &tracing.Tracer{Sampler: tracing.AlwaysSample{}, Export: sink}
	tr.Start()
	t.Cleanup(func() { tr.Stop(context.Background()) })
	return sink, tr
}

type spanSink struct {
	mu    sync.Mutex
	spans []*tracing.Span
}

func (s *spanSink) ExportSpans(_ context.Context, _, _ string, spans []*tracing.Span) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spans = append(s.spans, spans...)
	return nil
}

func (s *spanSink) all() []*tracing.Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*tracing.Span(nil), s.spans...)
}

func (s *spanSink) names() []string {
	out := []string{}
	for _, sp := range s.all() {
		out = append(out, sp.Name)
	}
	return out
}
