package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/tracing"
	"github.com/edlitmus/halite/internal/version"
)

// Tracing on the node: SPEC 26.3's spans, and where each one is started.
//
// Three spans, which is what the specification names: one per job, one
// per state inside it, and one per file transfer the states cause. They
// nest, so a slow highstate reads as the state that was slow, and a
// state that was slow because of the tree reads as the file that took
// the time.
//
// The parent comes off the job message. A hub that is tracing puts its
// dispatch span's `traceparent` there, so the node's job span continues
// the hub's trace rather than starting one beside it; a node running
// locally, or one whose hub is not tracing, starts a root instead.

// buildTracer reads the three settings and returns the tracer this
// process runs with, or nil.
//
// A configuration error is logged and tracing is left off. It is never
// fatal, and that is deliberate: telemetry is allowed to lose data and a
// node is not allowed to stop managing a machine because a collector URL
// has a typo in it. The operator is told, loudly, once, at startup --
// which is the difference between this and the silence the inert-key
// table used to describe.
func (n *node) buildTracer() {
	t, err := tracing.TracerFrom(tracing.Settings{
		Mode:          n.cfg.String("tracing", "off"),
		Endpoint:      n.cfg.String("tracing_endpoint", ""),
		SampleRate:    n.cfg.Float("tracing_sample_rate", tracing.DefaultSampleRate),
		SampleRateSet: n.cfg.IsSet("tracing_sample_rate"),
		Service:       "halite-node",
		Version:       version.String(),
	})
	if err != nil {
		n.log.Error("tracing is configured incorrectly and is off", "error", err.Error(), "component", "tracing")
		return
	}
	n.tracer = t
}

// startTracing begins the exporter and arranges for it to be drained.
//
// Called by whatever has a lifecycle long enough to have an end: the
// agent, and each one-shot command that runs something worth tracing.
// A tracer that is never started still hands out spans and still
// propagates them -- what it does not do is export, so starting it is
// not optional for the process that wants the data.
func (n *node) startTracing() {
	if n.tracer == nil {
		return
	}
	n.tracer.Start()
	traceShutdown = n.stopTracing
}

// stopTracing drains the queue.
//
// Bounded, because a node shutting down must not wait on a collector
// that has gone away: five seconds is long enough for a batch to reach a
// collector on the same network and short enough that nobody watching a
// service stop notices.
func (n *node) stopTracing() {
	if n == nil || n.tracer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n.tracer.Stop(ctx)
	if dropped := n.tracer.Dropped(); dropped > 0 {
		n.log.Warn("spans were dropped because the export queue was full",
			"dropped", dropped, "component", "tracing")
	}
}

// traceShutdown is what `exitWith` calls before the process ends.
//
// A package variable because `main` exits through `os.Exit`, which runs
// no deferred function: the flush has to be reachable from the exit
// itself. Nil until a command starts a tracer, and calling it is safe
// either way.
var traceShutdown func()

// exitWith ends the process, flushing whatever has to be flushed.
//
// Every `os.Exit` in this binary goes through here. A span exported by a
// long-running agent and not by a one-shot `state apply` would be the
// worst of both: the operator who turned tracing on to find out why a
// highstate is slow is running it by hand.
func exitWith(code int) {
	if traceShutdown != nil {
		traceShutdown()
	}
	os.Exit(code)
}

// jobSpan starts the span for one job, under whatever the hub sent.
//
// KindServer because the node is doing work something else asked for,
// which is what makes the hub's dispatch span and this one two halves of
// the same call rather than two unrelated roots.
func (n *node) jobSpan(j *job.Job) *tracing.Span {
	parent, _ := tracing.ParseTraceParent(j.TraceParent)
	s := n.tracer.StartSpan(parent, "job "+j.Fun, tracing.KindServer)
	s.SetAttr("halite.jid", string(j.JID))
	s.SetAttr("halite.fun", j.Fun)
	s.SetAttr("halite.node", n.nodeID)
	if j.Env != "" {
		s.SetAttr("halite.env", j.Env)
	}
	return s
}

// tracedFiles wraps the tree a run compiles against so that each fetch
// is a span.
//
// A decorator rather than a tracer inside internal/fileserver, because
// the two implementations of this interface -- the node's own roots and
// the hub's file server -- want the same span and only one of them
// performs a transfer. Wrapping the interface traces both, and neither
// package learns about tracing.
//
// What the span does not say is whether the hub answered 304. That
// distinction lives below this seam, inside `Remote.cacheFile`, and
// surfacing it would mean the FileFetcher interface reporting it -- a
// change to a seam shared by three implementations, for one attribute.
// The span still answers the question an operator has, which is which
// file cost the time.
type tracedFiles struct {
	inner  stateTree
	tracer *tracing.Tracer
	parent tracing.SpanContext
}

func (t tracedFiles) span(op, env, uri string) *tracing.Span {
	s := t.tracer.StartSpan(t.parent, "file "+op, tracing.KindClient)
	s.SetAttr("halite.file.env", env)
	s.SetAttr("halite.file.uri", uri)
	return s
}

func (t tracedFiles) Fetch(env, uri string) (string, error) {
	s := t.span("fetch", env, uri)
	defer s.End()
	path, err := t.inner.Fetch(env, uri)
	if err != nil {
		s.Fail(err)
		return path, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		s.SetAttr("halite.file.bytes", info.Size())
	}
	return path, nil
}

func (t tracedFiles) Hash(env, uri string) (string, string, error) {
	s := t.span("hash", env, uri)
	defer s.End()
	algorithm, digest, err := t.inner.Hash(env, uri)
	if err != nil {
		s.Fail(err)
	}
	return algorithm, digest, err
}

// Exists is not traced. It is answered from the cache in the common
// case and a span per existence check would bury the transfers in the
// checks that preceded them.
func (t tracedFiles) Exists(env, uri string) bool { return t.inner.Exists(env, uri) }

// The state.Loader half is passed straight through. Reading an SLS out
// of the tree is compilation rather than a transfer, and the compile is
// already timed as a whole; a span per included file would be a span
// per line of a top file.
func (t tracedFiles) Source(env, sls string) ([]byte, string, error) { return t.inner.Source(env, sls) }
func (t tracedFiles) Envs() []string                                 { return t.inner.Envs() }

func (t tracedFiles) Templates(env string) template.Loader { return t.inner.Templates(env) }

// ListUnder is what `file.recurse` reaches for. Traced, because it is a
// request to the hub like any other and a recursive copy of a large
// subtree is exactly the case somebody turns tracing on to understand.
func (t tracedFiles) ListUnder(env, prefix string) ([]string, error) {
	lister, ok := t.inner.(exec.FileLister)
	if !ok {
		return nil, fmt.Errorf("this file server cannot list %q", prefix)
	}
	s := t.span("list", env, prefix)
	defer s.End()
	out, err := lister.ListUnder(env, prefix)
	if err != nil {
		s.Fail(err)
		return nil, err
	}
	s.SetAttr("halite.file.count", len(out))
	return out, nil
}

// traceFiles wraps this node's tree for the span of one run.
//
// Returns the tree unchanged when nothing is being recorded, so an
// untraced run pays nothing -- not an indirection, not an allocation,
// and not a `os.Stat` per file.
func (n *node) traceFiles(parent *tracing.Span) stateTree {
	sc, ok := tracing.SpanContextOf(parent)
	if n.files == nil || n.tracer == nil || !ok || !sc.Sampled {
		return n.files
	}
	inner := n.files
	// The hub's file server makes its transfers under a context, and
	// giving it this run's puts the `traceparent` on the request -- so
	// the hub's side of a transfer is a child of the node's span rather
	// than a separate trace. It also attaches the job's cancellation to
	// the fetch, which is worth having on its own.
	if remote, ok := inner.(*fileserver.Remote); ok {
		inner = remote.WithContext(tracing.ContextWithSpan(context.Background(), parent))
	}
	return tracedFiles{inner: inner, tracer: n.tracer, parent: sc}
}

// compile-time check that the decorator still satisfies what a module
// is handed. A method added to FileFetcher and not to tracedFiles would
// otherwise be found by a state that used it.
var (
	_ exec.FileFetcher = tracedFiles{}
	_ exec.FileLister  = tracedFiles{}
	_ stateTree        = tracedFiles{}
)
