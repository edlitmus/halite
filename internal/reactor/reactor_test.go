package reactor

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/event"
	"github.com/edlitmus/halite/internal/transport"
)

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// recorder is a Dispatcher that remembers what it was asked to run.
type recorder struct {
	mu       sync.Mutex
	requests []transport.DispatchRequest
	by       []string
	fired    chan struct{}
}

func newRecorder() *recorder {
	return &recorder{fired: make(chan struct{}, 64)}
}

func (r *recorder) dispatch(req transport.DispatchRequest, by string) (transport.DispatchResponse, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.by = append(r.by, by)
	r.mu.Unlock()
	select {
	case r.fired <- struct{}{}:
	default:
	}
	return transport.DispatchResponse{JobID: "job1", Agents: []string{"web1"}}, nil
}

func (r *recorder) all() []transport.DispatchRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]transport.DispatchRequest(nil), r.requests...)
}

func (r *recorder) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if len(r.all()) >= n {
			return
		}
		select {
		case <-r.fired:
		case <-deadline:
			t.Fatalf("only %d dispatch(es) fired, want %d", len(r.all()), n)
		}
	}
}

func writeRules(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reactor.sls")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadParsesRules(t *testing.T) {
	path := writeRules(t, `'halite/agent/*/hello':
  - run:
      kind: state.highstate
      target: '{{ .Source }}'
'halite/beacon/*/service-down':
  - run:
      kind: call
      target: '{{ .Source }}'
      fn: service.running
      args:
        name: '{{ .Data.service }}'
      test: "true"
`)
	rules, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules))
	}
	if rules[0].Tag != "halite/agent/*/hello" || rules[0].Actions[0].Kind != "state.highstate" {
		t.Errorf("first rule = %+v", rules[0])
	}
	second := rules[1].Actions[0]
	if second.Fn != "service.running" || second.Args["name"] != "{{ .Data.service }}" || !second.Test {
		t.Errorf("second action = %+v", second)
	}
}

func TestLoadAcceptsAMissingFile(t *testing.T) {
	rules, err := Load(filepath.Join(t.TempDir(), "absent.sls"))
	if err != nil {
		t.Fatalf("a missing rules file must not be an error: %v", err)
	}
	if rules != nil {
		t.Errorf("got %v, want no rules", rules)
	}
	if rules, err := Load(""); err != nil || rules != nil {
		t.Errorf("an unset path must yield no rules: %v, %v", rules, err)
	}
}

func TestLoadRejectsMalformedRules(t *testing.T) {
	cases := map[string]string{
		"no actions":     "'halite/x': []\n",
		"unknown verb":   "'halite/x':\n  - explode:\n      kind: state.highstate\n",
		"missing kind":   "'halite/x':\n  - run:\n      target: web1\n",
		"missing target": "'halite/x':\n  - run:\n      kind: state.highstate\n",
		"unknown key":    "'halite/x':\n  - run:\n      kind: grains\n      target: web1\n      colour: blue\n",
		"not a list":     "'halite/x':\n  run: nope\n",
	}
	for name, content := range cases {
		if _, err := Load(writeRules(t, content)); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
}

func TestRuleFiresAndRendersTheEvent(t *testing.T) {
	rules := []Rule{{
		Tag: "halite/agent/*/hello",
		Actions: []Action{{
			Kind:   transport.KindHighstate,
			Target: "{{ .Source }}",
		}},
	}}
	rec := newRecorder()
	bus := event.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go New(rules, rec.dispatch, quietLogger()).Run(ctx, bus)
	time.Sleep(50 * time.Millisecond) // let the subscription land

	bus.Emit("halite/agent/web1/hello", "web1", map[string]any{"id": "web1"})
	rec.waitFor(t, 1)

	got := rec.all()[0]
	if got.Target != "web1" {
		t.Errorf("target = %q, want the event's source", got.Target)
	}
	if got.Kind != transport.KindHighstate {
		t.Errorf("kind = %q", got.Kind)
	}
	if rec.by[0] != "reactor" {
		t.Errorf("dispatched by %q, want reactor", rec.by[0])
	}
}

func TestRuleRendersEventData(t *testing.T) {
	rules := []Rule{{
		Tag: "halite/beacon/*/service-down",
		Actions: []Action{{
			Kind:   transport.KindCall,
			Target: "{{ .Source }}",
			Fn:     "service.running",
			Args:   map[string]string{"name": "{{ .Data.service }}"},
		}},
	}}
	rec := newRecorder()
	bus := event.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go New(rules, rec.dispatch, quietLogger()).Run(ctx, bus)
	time.Sleep(50 * time.Millisecond)

	bus.Emit("halite/beacon/web1/service-down", "web1", map[string]any{"service": "nginx"})
	rec.waitFor(t, 1)

	if got := rec.all()[0]; got.Args["name"] != "nginx" {
		t.Errorf("args = %v, want name=nginx from the event data", got.Args)
	}
}

func TestNonMatchingEventsAreIgnored(t *testing.T) {
	rules := []Rule{{
		Tag:     "halite/agent/*/hello",
		Actions: []Action{{Kind: transport.KindGrains, Target: "*"}},
	}}
	rec := newRecorder()
	bus := event.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go New(rules, rec.dispatch, quietLogger()).Run(ctx, bus)
	time.Sleep(50 * time.Millisecond)

	bus.Emit("halite/job/1/dispatch", "ed", nil)
	bus.Emit("halite/key/web1/pending", "master", nil)
	time.Sleep(200 * time.Millisecond)

	if fired := rec.all(); len(fired) != 0 {
		t.Errorf("fired on non-matching events: %v", fired)
	}
}

func TestReactorDoesNotReactToItsOwnWork(t *testing.T) {
	// A rule on job events is exactly the shape that loops: the job it
	// dispatches raises a dispatch event that matches the same rule.
	rules := []Rule{{
		Tag:     "halite/job/**",
		Actions: []Action{{Kind: transport.KindGrains, Target: "*"}},
	}}
	rec := newRecorder()
	bus := event.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go New(rules, rec.dispatch, quietLogger()).Run(ctx, bus)
	time.Sleep(50 * time.Millisecond)

	// An operator's dispatch: the reactor should answer this one.
	bus.Emit("halite/job/1/dispatch", "ed", map[string]any{"job_id": "1"})
	rec.waitFor(t, 1)

	// The reactor's own dispatch, marked as such: it must be ignored.
	bus.Emit("halite/job/2/dispatch", "reactor", map[string]any{"job_id": "2", "reactor": true})
	time.Sleep(200 * time.Millisecond)

	if fired := len(rec.all()); fired != 1 {
		t.Errorf("fired %d times, want 1 — reacted work must not feed the reactor", fired)
	}
}

func TestRateLimitStopsARunawayLoop(t *testing.T) {
	rules := []Rule{{
		Tag:     "halite/test",
		Actions: []Action{{Kind: transport.KindGrains, Target: "*"}},
	}}
	rec := newRecorder()
	engine := New(rules, rec.dispatch, quietLogger())
	engine.limit = 5

	for i := 0; i < 50; i++ {
		engine.react(event.Event{Tag: "halite/test", Source: "master"})
	}
	if fired := len(rec.all()); fired != 5 {
		t.Errorf("fired %d times, want the limit of 5", fired)
	}
}

func TestRateLimitRecoversInTheNextWindow(t *testing.T) {
	rules := []Rule{{
		Tag:     "halite/test",
		Actions: []Action{{Kind: transport.KindGrains, Target: "*"}},
	}}
	rec := newRecorder()
	engine := New(rules, rec.dispatch, quietLogger())
	engine.limit = 2

	for i := 0; i < 5; i++ {
		engine.react(event.Event{Tag: "halite/test"})
	}
	// Pretend a minute passed rather than waiting one.
	engine.mu.Lock()
	engine.window = time.Now().Add(-2 * time.Minute)
	engine.mu.Unlock()

	engine.react(event.Event{Tag: "halite/test"})
	if fired := len(rec.all()); fired != 3 {
		t.Errorf("fired %d times, want 3 (2 then 1 in a fresh window)", fired)
	}
}

func TestActionWithAnEmptyRenderedTargetIsRefused(t *testing.T) {
	// {{ .Data.host }} against an event with no such key renders empty; a
	// blank target must not become a dispatch.
	rules := []Rule{{
		Tag:     "halite/test",
		Actions: []Action{{Kind: transport.KindGrains, Target: "{{ .Data.host }}"}},
	}}
	rec := newRecorder()
	engine := New(rules, rec.dispatch, quietLogger())

	engine.react(event.Event{Tag: "halite/test", Data: map[string]any{}})
	if fired := rec.all(); len(fired) != 0 {
		t.Errorf("dispatched with an empty target: %v", fired)
	}
}

func TestRunStopsWithItsContext(t *testing.T) {
	rules := []Rule{{Tag: "halite/test", Actions: []Action{{Kind: "grains", Target: "*"}}}}
	bus := event.NewBus()
	ctx, cancel := context.WithCancel(context.Background())

	stopped := make(chan struct{})
	go func() {
		New(rules, newRecorder().dispatch, quietLogger()).Run(ctx, bus)
		close(stopped)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func TestNoRulesMeansNoSubscription(t *testing.T) {
	// Run must return immediately rather than hold a subscription open.
	done := make(chan struct{})
	go func() {
		New(nil, newRecorder().dispatch, quietLogger()).Run(context.Background(), event.NewBus())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run blocked with no rules loaded")
	}
}

func TestRenderErrorsAreReportedNotDispatched(t *testing.T) {
	var logged strings.Builder
	rules := []Rule{{
		Tag:     "halite/test",
		Actions: []Action{{Kind: transport.KindGrains, Target: "{{ .Nope"}},
	}}
	rec := newRecorder()
	engine := New(rules, rec.dispatch, log.New(&logged, "", 0))

	engine.react(event.Event{Tag: "halite/test", Source: "web1"})
	if fired := rec.all(); len(fired) != 0 {
		t.Errorf("a malformed template dispatched anyway: %v", fired)
	}
	if !strings.Contains(logged.String(), "reactor") {
		t.Errorf("the failure was not logged: %q", logged.String())
	}
}
