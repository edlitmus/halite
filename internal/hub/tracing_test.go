package hub

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/tracing"
	"github.com/edlitmus/halite/internal/transport"
)

// SPEC 26.3's propagation, checked where it actually has to work: a job
// dispatched by a tracing hub and read off a node's stream.
//
// Written this way rather than by calling Dispatch and inspecting the
// job, because the thing that can be wrong is the *message* — the field
// exists on the job and is dropped on the way to the wire, which is
// exactly the shape of the defect 4.13 recorded. Reading it off the
// stream is reading what a node would read.
func TestAJobCarriesTheHubsTraceToTheNode(t *testing.T) {
	l := newLab(t).withJobs(t)
	exported := &collectSpans{}
	l.server.Tracer = &tracing.Tracer{
		Service: "halite-hub",
		Sampler: tracing.AlwaysSample{},
		Export:  exported,
	}
	l.server.Tracer.Start()
	defer l.server.Tracer.Stop(context.Background())

	web1 := l.enrolled(t, "web1.example")
	seen := make(chan transport.Message, 4)
	defer l.watch(t, web1, "web1.example", seen)()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	})
	if err != nil {
		t.Fatal(err)
	}

	msg := awaitJob(t, seen)
	if msg.TraceParent == "" {
		t.Fatal("the hub is tracing and the job message carries no traceparent; " +
			"the node would start a second trace beside the hub's")
	}
	parent, ok := tracing.ParseTraceParent(msg.TraceParent)
	if !ok {
		t.Fatalf("the traceparent on the wire does not parse: %q", msg.TraceParent)
	}
	if !parent.Sampled {
		t.Error("the hub sampled this trace and told the node it had not")
	}

	// And it names the span the hub actually recorded, rather than some
	// other identifier that happens to be well formed.
	l.server.Tracer.Stop(context.Background())
	dispatch := exported.named("dispatch test.ping")
	if dispatch == nil {
		t.Fatalf("the hub exported no dispatch span; it exported %v", exported.names())
	}
	if dispatch.Context.TraceID != parent.TraceID {
		t.Errorf("the message names trace %s and the hub recorded %s",
			parent.TraceID, dispatch.Context.TraceID)
	}
	if dispatch.Context.SpanID != parent.SpanID {
		t.Errorf("the message names span %s and the hub recorded %s",
			parent.SpanID, dispatch.Context.SpanID)
	}
	if got := dispatch.Attributes()["halite.jid"]; got != res.JID {
		t.Errorf("the span records jid %v, the submission returned %v", got, res.JID)
	}
	if got := dispatch.Attributes()["halite.nodes"]; got != 1 {
		t.Errorf("the span records %v nodes", got)
	}
}

// A hub that is not tracing sends the message it always sent.
//
// The point of the assertion is the empty field rather than the absent
// span: a hub with tracing off must not put an all-zero traceparent on
// the wire, which a node would read as a parent that exists and hang
// its whole run off nothing.
func TestAHubThatIsNotTracingPutsNothingOnTheWire(t *testing.T) {
	l := newLab(t).withJobs(t)
	if l.server.Tracer != nil {
		t.Fatal("the lab's hub is tracing by default; the off case is the default and must be")
	}

	web1 := l.enrolled(t, "web1.example")
	seen := make(chan transport.Message, 4)
	defer l.watch(t, web1, "web1.example", seen)()

	op := l.operator(t, "ed")
	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	}); err != nil {
		t.Fatal(err)
	}

	msg := awaitJob(t, seen)
	if msg.TraceParent != "" {
		t.Errorf("an untraced hub sent traceparent %q", msg.TraceParent)
	}
	// Serialised, because "empty string" and "absent from the JSON" are
	// different things to an older node and only one of them is what
	// this build sent before tracing existed.
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes := string(raw); contains(bytes, "traceparent") {
		t.Errorf("an untraced hub's job message carries the field: %s", bytes)
	}
}

// awaitJob reads the job message off a watched stream.
func awaitJob(t *testing.T, seen <-chan transport.Message) transport.Message {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-seen:
			if msg.T == transport.MsgJob {
				return msg
			}
		case <-deadline:
			t.Fatal("no job reached the node")
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// watch subscribes and forwards every message, without answering.
//
// The lab's `connect` returns for each job, which is what most tests
// want and what this one must not do: the message is the subject here,
// and a return racing the assertion is a flake waiting for a slow day.
func (l *lab) watch(t *testing.T, client *transport.Client, nodeID string, out chan<- transport.Message) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Subscribe(ctx, transport.SubscribeRequest{
			NodeID: nodeID,
			Grains: json.RawMessage(`{"os":"FreeBSD"}`),
		}, func(msg transport.Message) error {
			if msg.T == transport.MsgPing {
				select {
				case <-ready:
				default:
					close(ready)
				}
				return nil
			}
			select {
			case out <- msg:
			default:
			}
			return nil
		})
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("%s never connected", nodeID)
	}
	return func() {
		cancel()
		<-stopped
	}
}

// collectSpans is an exporter that keeps what it is given.
type collectSpans struct {
	mu    sync.Mutex
	spans []*tracing.Span
}

func (c *collectSpans) ExportSpans(_ context.Context, _, _ string, spans []*tracing.Span) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spans = append(c.spans, spans...)
	return nil
}

func (c *collectSpans) named(name string) *tracing.Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.spans {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func (c *collectSpans) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.spans))
	for _, s := range c.spans {
		out = append(out, s.Name)
	}
	return out
}

var _ = job.ReturnSchema
