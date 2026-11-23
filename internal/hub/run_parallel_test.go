package hub

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// parallelByFun records, per function, whether the job that reached the
// node asked to run beside others. It is read at the node rather than on
// the hub's record, because the node is where the flag has to arrive.
type parallelByFun struct {
	mu  sync.Mutex
	saw map[string]bool
}

func (p *parallelByFun) record(msg transport.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.saw == nil {
		p.saw = map[string]bool{}
	}
	p.saw[msg.Fun] = msg.Parallel
}

func (p *parallelByFun) want(t *testing.T, fun string, want bool) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	got, ok := p.saw[fun]
	if !ok {
		t.Fatalf("no %s job reached the node; it saw %v", fun, p.saw)
	}
	if got != want {
		t.Errorf("%s reached the node with parallel=%v, want %v", fun, got, want)
	}
}

// A salt.function step with `run_parallel: true` sends a job that may run
// beside others on the node, as `halite-hub run --parallel` does (SPEC
// 9.6), and a step without it sends one that runs alone.
func TestARunParallelStepSendsAParallelJobAndOthersDoNot(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
warm_cache:
  salt.function:
    - name: test.ping
    - tgt: '*'
    - run_parallel: true

drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'

state_run_says_false:
  salt.state:
    - tgt: '*'
    - sls: [web]
    - run_parallel: false
`})
	var seen parallelByFun
	defer l.answeringTracking(t, "web1.example", seen.record)()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != OrchComplete {
		t.Fatalf("the run is %s: %+v", run.State, run.Steps)
	}
	seen.want(t, "test.ping", true)
	seen.want(t, "lb.drain", false)
	seen.want(t, "state.apply", false)
}

// A step that asks for something `run_parallel` cannot give, or writes it
// in a way that cannot be read, is refused when the orchestration
// compiles, before any step has been dispatched.
func TestRunParallelThatCannotBeHonouredIsRefusedAtCompile(t *testing.T) {
	cases := map[string]struct{ sls, want string }{
		"on a state step": {`
deploy:
  salt.state:
    - tgt: '*'
    - sls: [web]
    - run_parallel: true
`, "a state run always runs alone"},
		"on a highstate step": {`
deploy:
  salt.highstate:
    - tgt: '*'
    - run_parallel: true
`, "a state run always runs alone"},
		"on a function step that sends a state run": {`
deploy:
  salt.function:
    - name: state.apply
    - tgt: '*'
    - run_parallel: true
`, "a state run always runs alone"},
		"misspelt": {`
ping:
  salt.function:
    - name: test.ping
    - tgt: '*'
    - run_paralel: true
`, "run_paralel"},
		"not a boolean": {`
ping:
  salt.function:
    - name: test.ping
    - tgt: '*'
    - run_parallel: maybe
`, "run_parallel"},
		"on a runner step": {`
call:
  salt.runner:
    - name: jobs.active
    - run_parallel: true
`, "run_parallel"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			l := orchLab(t, map[string]string{"deploy.sls": c.sls})
			var seen parallelByFun
			defer l.answeringTracking(t, "web1.example", seen.record)()
			_, err := l.server.Orchestrate(context.Background(), OrchRequest{
				Principal: "cert:CN=ed", SLS: []string{"deploy"},
			})
			if err == nil {
				t.Fatal("the orchestration compiled")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal does not say %q: %v", c.want, err)
			}
			seen.mu.Lock()
			defer seen.mu.Unlock()
			if len(seen.saw) != 0 {
				t.Errorf("a refused orchestration dispatched %v", seen.saw)
			}
		})
	}
}

// `orch lint` is `state.orchestrate_show_sls`, which compiles and runs
// nothing. The refusal is part of compiling, so lint finds it too, which is
// what the handbook tells an author to rely on.
func TestOrchLintRefusesRunParallelOnAStateStep(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
deploy:
  salt.state:
    - tgt: '*'
    - sls: [web]
    - run_parallel: true
`})
	op := l.operator(t, "ed")
	res, err := op.Runner(context.Background(), transport.RunnerRequest{
		Fun: "state.orchestrate_show_sls", Kwarg: map[string]any{"sls": "deploy"},
	})
	var said string
	switch {
	case err != nil:
		said = err.Error()
	case !res.Success:
		said = res.Error
	default:
		t.Fatalf("lint accepted run_parallel on a state step: %s", res.Return)
	}
	if !strings.Contains(said, "a state run always runs alone") {
		t.Errorf("lint refused it, but not by saying why: %s", said)
	}
}

// A resume recompiles the orchestration from its SLS, so a step resumed
// after the hub restarts still asks for what it was written to ask for.
// The restart is the record store opened again from the same directory,
// which is all a restarted hub has of the earlier run.
func TestAResumedStepAfterARestartKeepsRunParallel(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'

warm:
  salt.function:
    - name: test.ping
    - tgt: '*'
    - run_parallel: true
    - require:
      - salt: drain
`})
	dir := t.TempDir()
	store, err := OpenOrchStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.server.Orch = store

	node := l.enrolled(t, "web1.example")
	stop := l.answeringAs(t, node, "web1.example", func(fun string) bool { return fun != "test.ping" })
	first, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.State != OrchFailed {
		t.Fatalf("the first run is %s; this test needs warm to fail", first.State)
	}
	stop()

	reopened, err := OpenOrchStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.server.Orch = reopened

	defer l.answeringAs(t, node, "web1.example", nil)()
	second, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
		ResumeOf: first.JID, ResumeFrom: "warm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != OrchComplete {
		t.Fatalf("the resumed run is %s: %+v", second.State, second.Steps)
	}

	// Read from the job record: it is what the node's message is built
	// from (TestTheParallelFlagReachesTheNode holds that half), and what a
	// batch resumed after a hub restart is delivered from (5.698).
	warm := stepByID(t, second, "warm")
	j, err := l.server.Jobs.Get(jobIDOf(warm.JID))
	if err != nil {
		t.Fatal(err)
	}
	if !j.Parallel {
		t.Error("the resumed step's job record does not say parallel")
	}
}

func parseReactionSource(t *testing.T, src string) ([]Reaction, error) {
	t.Helper()
	doc, _, err := yaml.Parse([]byte(src), yaml.Options{File: "r.sls"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := doc.(*value.Map)
	if !ok {
		t.Fatal("the fixture is not a mapping")
	}
	return parseReactions(m, "r.sls")
}

// A local or caller reaction with `run_parallel: true` dispatches a job
// that may run beside others; one without it dispatches one that runs
// alone. Read from the job record, which is what the node is sent.
func TestARunParallelReactionDispatchesAParallelJob(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.enrolled(t, "web1.example")
	reactions, err := parseReactionSource(t, `
local_parallel:
  local.test.ping:
    - tgt: web1.example
    - run_parallel: true
local_plain:
  local.test.ping:
    - tgt: web1.example
caller_parallel:
  caller.test.ping:
    - run_parallel: true
caller_plain:
  caller.test.ping: []
`)
	if err != nil {
		t.Fatal(err)
	}
	entry := ReactorEntry{Tag: "halite/test", Principal: "cert:CN=ed"}
	results := l.server.runReactions(context.Background(), entry,
		&eventbus.Event{Tag: "halite/test", Node: "web1.example"}, reactions)

	want := map[string]bool{
		"local_parallel": true, "local_plain": false,
		"caller_parallel": true, "caller_plain": false,
	}
	for _, res := range results {
		if !res.Succeeded() {
			t.Fatalf("%s failed: %s", res.Reaction.ID, res.Error)
		}
		j, err := l.server.Jobs.Get(jobIDOf(res.JID))
		if err != nil {
			t.Fatal(err)
		}
		if j.Parallel != want[res.Reaction.ID] {
			t.Errorf("%s dispatched a job with parallel=%v, want %v",
				res.Reaction.ID, j.Parallel, want[res.Reaction.ID])
		}
		delete(want, res.Reaction.ID)
	}
	if len(want) != 0 {
		t.Errorf("no result for %v", want)
	}
}

// A reaction's `run_parallel` that cannot be read, or cannot do what it
// says, is refused when the reaction is parsed rather than dropped.
func TestRunParallelThatCannotBeHonouredIsRefusedInAReaction(t *testing.T) {
	refused := map[string]struct{ src, want string }{
		"a string":    {"a:\n  local.test.ping:\n    - tgt: '*'\n    - run_parallel: 'no'\n", "true or false"},
		"an integer":  {"a:\n  local.test.ping:\n    - tgt: '*'\n    - run_parallel: 1\n", "true or false"},
		"misspelt":    {"a:\n  local.test.ping:\n    - tgt: '*'\n    - run_paralel: true\n", "run_paralel"},
		"on a runner": {"a:\n  runner.event.send:\n    - run_parallel: true\n", "sends none"},
		"false on a wheel": {"a:\n  wheel.key.list_all:\n    - run_parallel: false\n",
			"sends none"},
		"on a state run": {"a:\n  local.state.apply:\n    - tgt: '*'\n    - run_parallel: true\n",
			"always runs alone"},
		"on a caller state run": {"a:\n  caller.state.sls:\n    - run_parallel: true\n",
			"always runs alone"},
	}
	for name, c := range refused {
		_, err := parseReactionSource(t, c.src)
		if err == nil {
			t.Errorf("%s was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: the refusal does not say %q: %v", name, c.want, err)
		}
	}

	accepted := map[string]string{
		"false on a state run": "a:\n  local.state.apply:\n    - tgt: '*'\n    - run_parallel: false\n",
		"true on a function":   "a:\n  local.pkg.refresh_db:\n    - tgt: '*'\n    - run_parallel: true\n",
	}
	for name, src := range accepted {
		if _, err := parseReactionSource(t, src); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
}
