package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// The runner decides whether each chunk runs at all. Every rule in this
// file is one an operator relies on without thinking about it, and a
// mistake in any of them is a change made on a host that should not have
// been.

// probe records what actually ran, which is the only way to tell "skipped"
// from "ran and did nothing".
type probe struct {
	ran   []string
	runAs []string
	umask []string
}

// registries builds a state registry whose behaviour a test can script
// through the state's own arguments.
func registries(p *probe) (*states.Registry, *exec.Registry) {
	sr := states.NewRegistry()
	er := exec.NewRegistry()

	sig := func(module, function string) signature.Signature {
		return signature.Signature{
			Module: module, Function: function,
			Doc: "A scripted state, for exercising the runner.",
			Params: []signature.Param{
				{Name: "name", Type: signature.String},
				{Name: "changes", Type: signature.Bool, Default: false},
				{Name: "fail", Type: signature.Bool, Default: false},
				{Name: "watch_marker", Type: signature.String},
			},
			Mutates: true, TestMode: signature.TestReliable, Section: "test",
		}
	}

	run := func(c *exec.Context, args *value.Map) (states.Result, error) {
		name := states.Str(args, "name", "")
		// The per-state execution options are recorded so that a test can
		// assert they reached the module rather than stopping at the
		// unless and onlyif conditions.
		p.runAs = append(p.runAs, c.RunAs)
		p.umask = append(p.umask, c.Umask)
		// A test-mode invocation is recorded distinctly, because prereq
		// runs its target in test mode before deciding, and a trace that
		// hid that would look like the target ran twice.
		if c.Test {
			name += ":test"
		}
		p.ran = append(p.ran, name)
		if states.Bool(args, "fail", false) {
			return states.False("This state was scripted to fail."), nil
		}
		if !states.Bool(args, "changes", false) {
			return states.True("This state was scripted to change nothing."), nil
		}
		ch := value.MapOf("scripted", states.Change("before", "after"))
		if c.Test {
			return states.WouldChange("This state would change something.", ch), nil
		}
		return states.Changed("This state changed something.", ch), nil
	}

	watch := func(c *exec.Context, args *value.Map) (states.Result, error) {
		p.ran = append(p.ran, states.Str(args, "name", "")+":mod_watch")
		return states.Changed("The watch reaction ran.",
			value.MapOf("reacted", states.Change(false, true))), nil
	}

	sr.Add(
		states.Module{Sig: sig("probe", "run"), Fn: run, ModWatch: watch},
		states.Module{Sig: sig("probe", "plain"), Fn: run},
	)
	er.Add(exec.Module{
		Sig: signature.Signature{
			Module: "probe", Function: "answer",
			Doc:     "Return a scripted boolean, for exercising structured conditions.",
			Params:  []signature.Param{{Name: "value", Type: signature.Bool}},
			Section: "test",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			return states.Bool(args, "value", false), nil
		},
	})
	return sr, er
}

// compileAndRun compiles an SLS body and runs it, returning the result and
// what ran.
func compileAndRun(t *testing.T, sls string, opts ...func(*Runner)) (*RunResult, *probe) {
	t.Helper()
	p := &probe{}
	sr, er := registries(p)

	c := &state.Compiler{
		Loader:   memLoader{"base|web": sls},
		Registry: sr.Signatures(),
		Config:   state.Config{NodeID: "n", Grains: value.NewMap(0)},
	}
	compiled := c.CompileSLS([]string{"web"})
	if err := compiled.Err(); err != nil {
		t.Fatalf("compilation failed:\n%v", err)
	}

	r := &Runner{
		States: sr,
		Exec:   er,
		Ctx: &exec.Context{
			Ctx: context.Background(), Grains: value.NewMap(0),
			Pillar: value.NewMap(0), Config: value.NewMap(0),
			Runner: &exec.RecordingRunner{},
		},
		Sleep: func(time.Duration) {},
	}
	for _, o := range opts {
		o(r)
	}
	return r.Run(compiled.Low), p
}

type memLoader map[string]string

func (m memLoader) Source(env, sls string) ([]byte, string, error) {
	src, ok := m[env+"|"+sls]
	if !ok {
		return nil, "", state.ErrNotFound
	}
	return []byte(src), sls + ".sls", nil
}

func (m memLoader) Envs() []string { return []string{"base"} }

func (m memLoader) Templates(env string) template.Loader { return nil }

// resultFor finds one state's outcome by ID.
func resultFor(r *RunResult, id string) *StateResult {
	for _, res := range r.Results {
		if res.Chunk.ID == id {
			return res
		}
	}
	return nil
}

func ranContains(p *probe, name string) bool {
	for _, n := range p.ran {
		if n == name {
			return true
		}
	}
	return false
}

// ---- requisite gating ----

func TestRequireSkipsDependentsOfAFailure(t *testing.T) {
	out, p := compileAndRun(t, `
first:
  probe.run:
    - fail: true

second:
  probe.run:
    - require:
      - probe: first
`)
	if !resultFor(out, "first").Result.Failed() {
		t.Fatal("the first state should have failed")
	}
	second := resultFor(out, "second")
	if !second.Skipped || !second.Result.Failed() {
		t.Errorf("the dependent should be skipped and marked failed: %+v", second.Result)
	}
	// The comment names what failed, which is what an operator reads.
	if !strings.Contains(second.Result.Comment, "first") {
		t.Errorf("comment = %q", second.Result.Comment)
	}
	if ranContains(p, "second") {
		t.Error("the dependent ran despite its requisite failing")
	}
}

func TestRequireAnyNeedsOneSuccess(t *testing.T) {
	out, _ := compileAndRun(t, `
a:
  probe.run:
    - fail: true

b:
  probe.run: []

dependent:
  probe.run:
    - require_any:
      - probe: a
      - probe: b
`)
	if resultFor(out, "dependent").Skipped {
		t.Error("require_any should be satisfied by one success")
	}

	out, _ = compileAndRun(t, `
a:
  probe.run:
    - fail: true

b:
  probe.run:
    - fail: true

dependent:
  probe.run:
    - require_any:
      - probe: a
      - probe: b
`)
	if !resultFor(out, "dependent").Skipped {
		t.Error("require_any with no success should skip")
	}
}

func TestWatchRunsModWatchOnlyWhenTheTargetChanged(t *testing.T) {
	out, p := compileAndRun(t, `
source:
  probe.run:
    - changes: true

reactor:
  probe.run:
    - watch:
      - probe: source
`)
	if !ranContains(p, "reactor:mod_watch") {
		t.Errorf("the watch reaction did not run: %v", p.ran)
	}
	if ranContains(p, "reactor") {
		t.Errorf("the normal function ran instead of the reaction: %v", p.ran)
	}
	if !resultFor(out, "reactor").Result.HasChanges() {
		t.Error("the reaction reported no changes")
	}

	// No change in the target means the normal function, not the
	// reaction.
	_, p = compileAndRun(t, `
source:
  probe.run:
    - changes: false

reactor:
  probe.run:
    - watch:
      - probe: source
`)
	if ranContains(p, "reactor:mod_watch") {
		t.Errorf("the reaction ran without a change: %v", p.ran)
	}
	if !ranContains(p, "reactor") {
		t.Errorf("the normal function did not run: %v", p.ran)
	}
}

func TestOnChangesSkipsWithSuccess(t *testing.T) {
	// Not needing to run is not a fault: the skip is a success, so a
	// state that depends on it is not itself skipped.
	out, p := compileAndRun(t, `
quiet:
  probe.run:
    - changes: false

reacts:
  probe.run:
    - onchanges:
      - probe: quiet

after:
  probe.run:
    - require:
      - probe: reacts
`)
	reacts := resultFor(out, "reacts")
	if !reacts.Skipped {
		t.Error("onchanges with no change should skip")
	}
	if reacts.Result.Failed() {
		t.Error("an onchanges skip is a success, not a failure")
	}
	if !ranContains(p, "after") {
		t.Errorf("a state depending on the skipped one should still run: %v", p.ran)
	}
}

func TestOnChangesAllVersusAny(t *testing.T) {
	// onchanges needs every target to have changed; onchanges_any needs
	// one.
	body := `
changed:
  probe.run:
    - changes: true

quiet:
  probe.run:
    - changes: false

needs_all:
  probe.run:
    - onchanges:
      - probe: changed
      - probe: quiet

needs_any:
  probe.run:
    - onchanges_any:
      - probe: changed
      - probe: quiet
`
	out, _ := compileAndRun(t, body)
	if !resultFor(out, "needs_all").Skipped {
		t.Error("onchanges should skip when only one target changed")
	}
	if resultFor(out, "needs_any").Skipped {
		t.Error("onchanges_any should run when one target changed")
	}
}

func TestOnFailRunsOnlyOnFailure(t *testing.T) {
	out, p := compileAndRun(t, `
deploy:
  probe.run:
    - fail: true

rollback:
  probe.run:
    - onfail:
      - probe: deploy
`)
	if resultFor(out, "rollback").Skipped {
		t.Error("onfail should run when its target failed")
	}
	if !ranContains(p, "rollback") {
		t.Errorf("the rollback did not run: %v", p.ran)
	}

	// And is skipped, as a success, when nothing failed.
	out, p = compileAndRun(t, `
deploy:
  probe.run: []

rollback:
  probe.run:
    - onfail:
      - probe: deploy
`)
	r := resultFor(out, "rollback")
	if !r.Skipped || r.Result.Failed() {
		t.Errorf("onfail with no failure should skip as a success: %+v", r.Result)
	}
	if ranContains(p, "rollback") {
		t.Errorf("the rollback ran with nothing failed: %v", p.ran)
	}
}

func TestOnFailAllNeedsEveryTargetToFail(t *testing.T) {
	out, _ := compileAndRun(t, `
a:
  probe.run:
    - fail: true

b:
  probe.run: []

all_failed:
  probe.run:
    - onfail_all:
      - probe: a
      - probe: b

any_failed:
  probe.run:
    - onfail_any:
      - probe: a
      - probe: b
`)
	if !resultFor(out, "all_failed").Skipped {
		t.Error("onfail_all should skip when only one target failed")
	}
	if resultFor(out, "any_failed").Skipped {
		t.Error("onfail_any should run when one target failed")
	}
}

// TestPrereqRunsOnlyWhenTheTargetWouldChange is SPEC section 11.5: the
// target is run in test mode, its result discarded except for the changes
// prediction, and the prereq runs before it.
func TestPrereqRunsOnlyWhenTheTargetWouldChange(t *testing.T) {
	out, p := compileAndRun(t, `
target:
  probe.run:
    - changes: true

prep:
  probe.run:
    - prereq:
      - probe: target
`)
	if resultFor(out, "prep").Skipped {
		t.Error("prereq should run when its target would change")
	}
	// The target is probed in test mode first, which is how the decision
	// is made; then prep runs, and only then the target for real. That
	// ordering is what makes prereq different from require.
	want := []string{"target:test", "prep", "target"}
	if len(p.ran) != 3 {
		t.Fatalf("ran %v, want %v", p.ran, want)
	}
	for i := range want {
		if p.ran[i] != want[i] {
			t.Fatalf("ran %v, want %v", p.ran, want)
		}
	}

	// A target that would not change skips the prereq, as a success.
	out, p = compileAndRun(t, `
target:
  probe.run:
    - changes: false

prep:
  probe.run:
    - prereq:
      - probe: target
`)
	prep := resultFor(out, "prep")
	if !prep.Skipped || prep.Result.Failed() {
		t.Errorf("prereq with an unchanging target should skip as a success: %+v", prep.Result)
	}
	if ranContains(p, "prep") {
		t.Errorf("the prereq ran: %v", p.ran)
	}
	// The target was still probed in test mode, and then ran for real.
	if !ranContains(p, "target:test") || !ranContains(p, "target") {
		t.Errorf("ran %v", p.ran)
	}
}

func TestListenRunsAtTheEndOfTheRun(t *testing.T) {
	out, p := compileAndRun(t, `
source:
  probe.run:
    - changes: true

listener:
  probe.run:
    - listen:
      - probe: source

after_source:
  probe.run:
    - require:
      - probe: source
`)
	// The reaction is appended after everything else, which is what
	// distinguishes listen from watch.
	last := out.Results[len(out.Results)-1]
	if last.Chunk.ID != "listener" || !last.Result.HasChanges() {
		t.Errorf("the last result should be the listen reaction, got %s", last.Chunk.ID)
	}
	if !strings.Contains(last.Result.Comment, "end of the state run") {
		t.Errorf("comment = %q", last.Result.Comment)
	}
	if !ranContains(p, "listener:mod_watch") {
		t.Errorf("the listen reaction did not run: %v", p.ran)
	}
}

func TestListenDoesNotFireWithoutAChange(t *testing.T) {
	_, p := compileAndRun(t, `
source:
  probe.run:
    - changes: false

listener:
  probe.run:
    - listen:
      - probe: source
`)
	if ranContains(p, "listener:mod_watch") {
		t.Errorf("the listen reaction fired without a change: %v", p.ran)
	}
}

// ---- the universal gates ----

func TestCreatesSkipsWhenThePathExists(t *testing.T) {
	dir := t.TempDir()
	out, p := compileAndRun(t, `
guarded:
  probe.run:
    - creates: `+dir+`
`)
	r := resultFor(out, "guarded")
	if !r.Skipped || r.Result.Failed() {
		t.Errorf("creates should skip as a success: %+v", r.Result)
	}
	if !strings.Contains(r.Result.Comment, "already exists") {
		t.Errorf("comment = %q", r.Result.Comment)
	}
	if ranContains(p, "guarded") {
		t.Errorf("the state ran: %v", p.ran)
	}

	// A path that is not there does not gate.
	_, p = compileAndRun(t, `
guarded:
  probe.run:
    - creates: `+dir+`/absent
`)
	if !ranContains(p, "guarded") {
		t.Errorf("the state should have run: %v", p.ran)
	}
}

func TestUnlessAndOnlyIfThroughAStructuredCondition(t *testing.T) {
	// The structured form avoids a shell entirely, which is why SPEC
	// section 11.7 prefers it.
	out, p := compileAndRun(t, `
skipped:
  probe.run:
    - unless:
        fun: probe.answer
        kwargs:
          value: true

runs:
  probe.run:
    - unless:
        fun: probe.answer
        kwargs:
          value: false

gated_out:
  probe.run:
    - onlyif:
        fun: probe.answer
        kwargs:
          value: false
`)
	if !resultFor(out, "skipped").Skipped {
		t.Error("a satisfied unless should skip")
	}
	if resultFor(out, "runs").Skipped {
		t.Error("an unsatisfied unless should not skip")
	}
	if !resultFor(out, "gated_out").Skipped {
		t.Error("an unmet onlyif should skip")
	}
	if ranContains(p, "skipped") || ranContains(p, "gated_out") {
		t.Errorf("a gated state ran: %v", p.ran)
	}
}

func TestConditionErrorsFailRatherThanGuess(t *testing.T) {
	out, _ := compileAndRun(t, `
bad:
  probe.run:
    - unless:
        fun: nosuch.function
`)
	r := resultFor(out, "bad")
	if !r.Result.Failed() {
		t.Error("a condition that cannot be evaluated must fail the state, not be assumed")
	}
	if !strings.Contains(r.Result.Comment, "could not be evaluated") {
		t.Errorf("comment = %q", r.Result.Comment)
	}
}

// ---- retry, check_cmd, failhard ----

func TestRetryStopsWhenTheStateSucceeds(t *testing.T) {
	// The scripted state always fails, so the retry runs its full budget
	// and the count is observable.
	_, p := compileAndRun(t, `
flaky:
  probe.run:
    - fail: true
    - retry:
        attempts: 3
        interval: 1
`)
	count := 0
	for _, n := range p.ran {
		if n == "flaky" {
			count++
		}
	}
	if count != 3 {
		t.Errorf("the state ran %d times, want 3 attempts", count)
	}

	// A state that succeeds first time is not retried.
	_, p = compileAndRun(t, `
fine:
  probe.run:
    - retry:
        attempts: 3
`)
	count = 0
	for _, n := range p.ran {
		if n == "fine" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("a succeeding state ran %d times, want 1", count)
	}
}

func TestFailHardStopsTheRun(t *testing.T) {
	out, p := compileAndRun(t, `
first:
  probe.run:
    - fail: true
    - failhard: true

second:
  probe.run: []

third:
  probe.run: []
`)
	if !out.Aborted || out.AbortedBy != "first" {
		t.Errorf("the run should have aborted on first: aborted=%v by=%q", out.Aborted, out.AbortedBy)
	}
	if ranContains(p, "second") || ranContains(p, "third") {
		t.Errorf("states ran after failhard: %v", p.ran)
	}
	// The unattempted states are still reported, saying why.
	second := resultFor(out, "second")
	if second == nil || !strings.Contains(second.Result.Comment, "aborted by failhard") {
		t.Errorf("second = %+v", second)
	}
}

func TestGlobalFailHardAppliesWithoutAPerStateOption(t *testing.T) {
	out, _ := compileAndRun(t, `
first:
  probe.run:
    - fail: true

second:
  probe.run: []
`, func(r *Runner) { r.FailHard = true })
	if !out.Aborted {
		t.Error("the global failhard setting was not applied")
	}
}

func TestPerStateFailHardOverridesTheGlobalSetting(t *testing.T) {
	out, p := compileAndRun(t, `
first:
  probe.run:
    - fail: true
    - failhard: false

second:
  probe.run: []
`, func(r *Runner) { r.FailHard = true })
	if out.Aborted {
		t.Error("failhard: false should override the global setting")
	}
	if !ranContains(p, "second") {
		t.Errorf("the run stopped anyway: %v", p.ran)
	}
}

// ---- results and the return schema ----

func TestRetCodeSemantics(t *testing.T) {
	// SPEC section 11.4: 0 all succeeded with changes, 1 one or more
	// failed, 2 succeeded with no changes required.
	out, _ := compileAndRun(t, "a:\n  probe.run:\n    - changes: true\n")
	if out.RetCode() != 0 {
		t.Errorf("changed run = %d, want 0", out.RetCode())
	}
	out, _ = compileAndRun(t, "a:\n  probe.run: []\n")
	if out.RetCode() != 2 {
		t.Errorf("unchanged run = %d, want 2", out.RetCode())
	}
	out, _ = compileAndRun(t, "a:\n  probe.run:\n    - fail: true\n")
	if out.RetCode() != 1 {
		t.Errorf("failed run = %d, want 1", out.RetCode())
	}
}

func TestReturnSchemaShape(t *testing.T) {
	out, _ := compileAndRun(t, "nginx:\n  probe.run:\n    - changes: true\n")
	returns := out.Returns()
	if returns.Len() != 1 {
		t.Fatalf("returns = %d", returns.Len())
	}
	// The key format is Salt's, and it is load-bearing.
	key := "probe_|-nginx_|-nginx_|-run"
	entry, ok := returns.Get(key)
	if !ok {
		t.Fatalf("the return key is not %q: %v", key, returns.StringKeys())
	}
	m := entry.(*value.Map)
	for _, field := range []string{"__id__", "__sls__", "__run_num__", "name", "result", "changes", "comment", "duration", "start_time", "warnings"} {
		if !m.Has(field) {
			t.Errorf("the return is missing %q", field)
		}
	}
	if got, _ := m.Get("result"); got != true {
		t.Errorf("result = %#v", got)
	}
}

func TestTestModeResultSurvivesAsNull(t *testing.T) {
	// A nil result is test mode's "would change" and must not collapse to
	// false in the return, or a dashboard reads it as a failure.
	p := &probe{}
	sr, er := registries(p)
	c := &state.Compiler{
		Loader:   memLoader{"base|web": "a:\n  probe.run:\n    - changes: true\n"},
		Registry: sr.Signatures(),
		Config:   state.Config{NodeID: "n", Grains: value.NewMap(0)},
	}
	compiled := c.CompileSLS([]string{"web"})
	r := &Runner{
		States: sr, Exec: er,
		Ctx: &exec.Context{
			Ctx: context.Background(), Grains: value.NewMap(0),
			Pillar: value.NewMap(0), Config: value.NewMap(0), Test: true,
		},
		Sleep: func(time.Duration) {},
	}
	out := r.Run(compiled.Low)
	entry, _ := out.Returns().Get("probe_|-a_|-a_|-run")
	got, _ := entry.(*value.Map).Get("result")
	if got != nil {
		t.Errorf("result = %#v, want null", got)
	}
	if s := out.Summarise(); s.WouldHave != 1 {
		t.Errorf("summary = %+v, want one would-change", s)
	}
}

func TestSummaryAndNestedRendering(t *testing.T) {
	out, _ := compileAndRun(t, `
changed:
  probe.run:
    - changes: true

failed:
  probe.run:
    - fail: true

skipped:
  probe.run:
    - onchanges:
      - probe: failed
`)
	s := out.Summarise()
	if s.Total != 3 || s.Failed != 1 || s.Changed != 1 {
		t.Errorf("summary = %+v", s)
	}
	line := s.String()
	for _, want := range []string{"Succeeded", "Failed", "Total", "Duration"} {
		if !strings.Contains(line, want) {
			t.Errorf("summary line is missing %q: %s", want, line)
		}
	}

	nested := out.Nested(false)
	for _, want := range []string{"ID: changed", "Function: probe.run", "Result:", "Comment:", "Changes:", "Summary"} {
		if !strings.Contains(nested, want) {
			t.Errorf("nested output is missing %q:\n%s", want, nested)
		}
	}
}

func TestEnvelopeCarriesTheJobFields(t *testing.T) {
	out, _ := compileAndRun(t, "a:\n  probe.run:\n    - changes: true\n")
	env := out.Envelope(JobReturn{
		JID: "20260820T101010000000", NodeID: "web1.prod",
		Fun: "state.apply", FunArgs: []string{"web"},
		StartTime: time.Now(),
	})
	for _, field := range []string{"jid", "id", "fun", "fun_args", "success", "retcode", "return", "out", "start_time", "duration_ms", "node_version", "schema"} {
		if !env.Has(field) {
			t.Errorf("the envelope is missing %q", field)
		}
	}
	if got, _ := env.Get("schema"); got != "halite.ret/1" {
		t.Errorf("schema = %#v", got)
	}
	if got, _ := env.Get("success"); got != true {
		t.Errorf("success = %#v", got)
	}
}

func TestProgressIsReportedForEveryChunk(t *testing.T) {
	var seen []string
	compileAndRun(t, "a:\n  probe.run: []\nb:\n  probe.run: []\n", func(r *Runner) {
		r.Progress = func(ch *state.Chunk, i, total int) {
			seen = append(seen, ch.ID)
			if total != 2 {
				t.Errorf("total = %d", total)
			}
		}
	})
	if len(seen) != 2 || seen[0] != "a" {
		t.Errorf("progress = %v", seen)
	}
}

func TestAStateThatErrorsIsReportedNotPanicked(t *testing.T) {
	// A module that returns an error becomes a failed state with the
	// error in its comment, rather than taking the run down.
	sr := states.NewRegistry()
	sr.Add(states.Module{
		Sig: signature.Signature{
			Module: "boom", Function: "now", Doc: "Always errors.",
			Params:  []signature.Param{{Name: "name", Type: signature.String}},
			Mutates: true, Section: "test",
		},
		Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
			return states.Result{}, context.DeadlineExceeded
		},
	})
	c := &state.Compiler{
		Loader:   memLoader{"base|web": "a:\n  boom.now: []\n"},
		Registry: sr.Signatures(),
		Config:   state.Config{NodeID: "n", Grains: value.NewMap(0)},
	}
	compiled := c.CompileSLS([]string{"web"})
	r := &Runner{
		States: sr, Exec: exec.NewRegistry(),
		Ctx:   &exec.Context{Ctx: context.Background(), Grains: value.NewMap(0), Pillar: value.NewMap(0)},
		Sleep: func(time.Duration) {},
	}
	out := r.Run(compiled.Low)
	res := resultFor(out, "a")
	if !res.Result.Failed() || !strings.Contains(res.Result.Comment, "raised an error") {
		t.Errorf("result = %+v", res.Result)
	}
}

func TestEmptyRun(t *testing.T) {
	r := &Runner{States: states.NewRegistry(), Exec: exec.NewRegistry(),
		Ctx: &exec.Context{Ctx: context.Background()}}
	out := r.Run(nil)
	if out.Failed() || out.Changed() {
		t.Error("an empty run should be neither failed nor changed")
	}
	if out.RetCode() != 2 {
		t.Errorf("an empty run should exit 2, got %d", out.RetCode())
	}
}

// SPEC section 11.7 lists runas and umask as per-state options. They were
// applied to a state's unless and onlyif conditions but not to the state
// itself, which is the wrong half: a cmd.run asking for umask 077 wrote a
// world-readable file and reported success.
//
// The probe module declares neither, which is the point: a per-state
// option is available on any state, so it must not require the module to
// have a parameter of the same name.
func TestPerStateExecutionOptionsReachTheModule(t *testing.T) {
	_, p := compileAndRun(t, `
masked:
  probe.run:
    - umask: '077'
    - runas: someone

bare:
  probe.run: []
`)
	if len(p.ran) != 2 {
		t.Fatalf("ran = %v", p.ran)
	}
	// The states run in declaration order, so index 0 is the masked one.
	if p.umask[0] != "077" {
		t.Errorf("umask reached the module as %q, want 077", p.umask[0])
	}
	if p.runAs[0] != "someone" {
		t.Errorf("runas reached the module as %q, want someone", p.runAs[0])
	}
	// A state that asked for neither must not inherit its neighbour's,
	// which is what sharing one context between chunks would do.
	if p.umask[1] != "" || p.runAs[1] != "" {
		t.Errorf("a state with no options saw umask %q and runas %q", p.umask[1], p.runAs[1])
	}
}
