package orch

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

	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/transport"
)

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// fleet is a stand-in control plane: it records dispatches and answers
// them with whatever outcome the test asked for.
type fleet struct {
	mu         sync.Mutex
	dispatched []transport.DispatchRequest
	order      []string
	agents     []string        // who each dispatch matches
	failFor    map[string]bool // step id -> the agents fail
	silentFor  map[string]bool // step id -> no agent ever answers
	jobs       map[string]transport.JobInfo
	nextJob    int
}

func newFleet() *fleet {
	return &fleet{
		agents:    []string{"web1"},
		failFor:   map[string]bool{},
		silentFor: map[string]bool{},
		jobs:      map[string]transport.JobInfo{},
	}
}

func (f *fleet) dispatch(req transport.DispatchRequest, by string) (transport.DispatchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextJob++
	id := "job" + string(rune('0'+f.nextJob))
	f.dispatched = append(f.dispatched, req)

	agents := f.agents
	if len(agents) == 0 {
		return transport.DispatchResponse{JobID: id}, nil
	}
	// The step id is not on the wire, so tests key behaviour off the
	// command they asked the agents to run.
	step := req.Args["name"]
	f.order = append(f.order, step)
	if f.silentFor[step] {
		f.jobs[id] = transport.JobInfo{Expecting: agents}
		return transport.DispatchResponse{JobID: id, Agents: agents}, nil
	}
	var results []transport.JobResult
	for _, agent := range agents {
		results = append(results, transport.JobResult{
			JobID: id, AgentID: agent, Ok: !f.failFor[step], Changed: 1, Succeeded: 1,
		})
	}
	f.jobs[id] = transport.JobInfo{Expecting: agents, Results: results}
	return transport.DispatchResponse{JobID: id, Agents: agents}, nil
}

func (f *fleet) jobInfo(id string) (transport.JobInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.jobs[id]
	return info, ok
}

func (f *fleet) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

func (f *fleet) runner() *Runner {
	return &Runner{
		Dispatch:    f.dispatch,
		Jobs:        f.jobInfo,
		Emit:        func(string, map[string]any) {},
		Log:         quietLogger(),
		StepTimeout: 2 * time.Second,
		By:          "orchestrator",
	}
}

// steps compiles an orchestration from source, the way the control plane
// does, so the tests exercise the real SLS pipeline.
func steps(t *testing.T, content string) []sls.State {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "orch.sls"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := (&sls.Loader{Root: root}).LoadNames([]string{"orch"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return loaded
}

const ordered = `first:
  halite.run:
    - target: 'lb*'
    - kind: call
    - fn: cmd.run
    - args:
        name: drain

second:
  halite.run:
    - target: 'web*'
    - kind: call
    - fn: cmd.run
    - args:
        name: upgrade
    - require:
      - halite: first

third:
  halite.run:
    - target: 'lb*'
    - kind: call
    - fn: cmd.run
    - args:
        name: restore
    - require:
      - halite: second
`

func TestStepsRunInRequisiteOrder(t *testing.T) {
	f := newFleet()
	outcomes := f.runner().Run(context.Background(), "orch1", steps(t, ordered))

	if got := strings.Join(f.ran(), ","); got != "drain,upgrade,restore" {
		t.Errorf("ran %q, want drain,upgrade,restore", got)
	}
	if len(outcomes) != 3 {
		t.Fatalf("got %d outcomes, want 3", len(outcomes))
	}
	for _, outcome := range outcomes {
		if !outcome.Ok {
			t.Errorf("step %s failed: %s", outcome.ID, outcome.Comment)
		}
		if outcome.JobID == "" || len(outcome.Agents) == 0 {
			t.Errorf("step %s lost its job details: %+v", outcome.ID, outcome)
		}
	}
}

func TestAFailedStepStopsWhatDependsOnIt(t *testing.T) {
	f := newFleet()
	f.failFor["drain"] = true

	outcomes := f.runner().Run(context.Background(), "orch1", steps(t, ordered))

	if got := strings.Join(f.ran(), ","); got != "drain" {
		t.Errorf("ran %q; nothing after the failure should have run", got)
	}
	byID := map[string]StepOutcome{}
	for _, outcome := range outcomes {
		byID[outcome.ID] = outcome
	}
	if byID["first"].Ok {
		t.Error("the failing step reported success")
	}
	if byID["second"].Ok || !strings.Contains(byID["second"].Comment, "requisite") {
		t.Errorf("dependent step = %+v, want a requisite failure", byID["second"])
	}
	if byID["third"].Ok {
		t.Error("a step two levels down still ran")
	}
}

func TestAStepMatchingNoAgentsFails(t *testing.T) {
	// Silently succeeding here is the dangerous behaviour: a drain step
	// that reached no load balancer must not let the upgrade proceed.
	f := newFleet()
	f.agents = nil

	outcomes := f.runner().Run(context.Background(), "orch1", steps(t, ordered))
	if outcomes[0].Ok {
		t.Fatal("a step that matched no agents reported success")
	}
	if !strings.Contains(outcomes[0].Comment, "no online agents") {
		t.Errorf("comment = %q", outcomes[0].Comment)
	}
	if len(f.ran()) != 0 {
		t.Errorf("something ran anyway: %v", f.ran())
	}
}

func TestAStepTimesOutWaitingForAgents(t *testing.T) {
	f := newFleet()
	f.silentFor["drain"] = true
	runner := f.runner()
	runner.StepTimeout = 300 * time.Millisecond

	outcomes := runner.Run(context.Background(), "orch1", steps(t, ordered))
	if outcomes[0].Ok {
		t.Fatal("a step whose agents never answered reported success")
	}
	if !strings.Contains(outcomes[0].Comment, "timed out") {
		t.Errorf("comment = %q", outcomes[0].Comment)
	}
	if got := strings.Join(f.ran(), ","); got != "drain" {
		t.Errorf("ran %q; the timeout should have stopped the run", got)
	}
}

func TestStepsBuildTheRightDispatch(t *testing.T) {
	f := newFleet()
	f.runner().Run(context.Background(), "orch1", steps(t, `apply_web:
  halite.run:
    - target: 'web*'
    - kind: state.apply
    - sls:
      - web.nginx
      - web.tls
`))
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.dispatched) != 1 {
		t.Fatalf("got %d dispatches, want 1", len(f.dispatched))
	}
	req := f.dispatched[0]
	if req.Target != "web*" || req.Kind != transport.KindApply {
		t.Errorf("dispatch = %+v", req)
	}
	if strings.Join(req.SLS, ",") != "web.nginx,web.tls" {
		t.Errorf("sls = %v", req.SLS)
	}
}

func TestHighstateIsTheDefaultKind(t *testing.T) {
	f := newFleet()
	f.runner().Run(context.Background(), "orch1", steps(t, `converge:
  halite.run:
    - target: '*'
`))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dispatched[0].Kind != transport.KindHighstate {
		t.Errorf("kind = %q, want %q", f.dispatched[0].Kind, transport.KindHighstate)
	}
}

func TestMalformedStepsFailWithoutDispatching(t *testing.T) {
	cases := map[string]string{
		"no target":    "bad:\n  halite.run:\n    - kind: grains\n",
		"unknown kind": "bad:\n  halite.run:\n    - target: '*'\n    - kind: explode\n",
		"apply no sls": "bad:\n  halite.run:\n    - target: '*'\n    - kind: state.apply\n",
		"call no fn":   "bad:\n  halite.run:\n    - target: '*'\n    - kind: call\n",
	}
	for name, content := range cases {
		f := newFleet()
		outcomes := f.runner().Run(context.Background(), "orch1", steps(t, content))
		if outcomes[0].Ok {
			t.Errorf("%s: reported success", name)
		}
		if len(f.dispatched) != 0 {
			t.Errorf("%s: dispatched anyway: %v", name, f.dispatched)
		}
	}
}

func TestTestModeIsPassedToEveryStep(t *testing.T) {
	f := newFleet()
	compiled := steps(t, ordered)
	for i := range compiled {
		compiled[i].Args["test"] = "true"
	}
	f.runner().Run(context.Background(), "orch1", compiled)

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.dispatched {
		if !req.Test {
			t.Errorf("step %q dispatched without test mode", req.Args["name"])
		}
	}
}

func TestRunEmitsProgress(t *testing.T) {
	f := newFleet()
	var (
		mu   sync.Mutex
		tags []string
	)
	runner := f.runner()
	runner.Emit = func(tag string, _ map[string]any) {
		mu.Lock()
		tags = append(tags, tag)
		mu.Unlock()
	}
	runner.Run(context.Background(), "orch1", steps(t, `only:
  halite.run:
    - target: '*'
    - kind: grains
`))

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(tags, " ")
	for _, want := range []string{
		"halite/orch/orch1/start",
		"halite/orch/orch1/step/only",
		"halite/orch/orch1/done",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %q among %v", want, tags)
		}
	}
}

func TestRunStopsWithItsContext(t *testing.T) {
	f := newFleet()
	f.silentFor["drain"] = true
	runner := f.runner()
	runner.StepTimeout = time.Hour // only the context can end this

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx, "orch1", steps(t, ordered))
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
