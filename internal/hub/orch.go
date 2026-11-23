package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fnmatch"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/orchplan"
	"github.com/edlitmus/halite/internal/runner"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// OrchRequest is one orchestration to run.
type OrchRequest struct {
	Principal string
	// OnBehalfOf is the operator Principal runs it for, already accepted
	// by checkDelegation; every step is authorized for them as well.
	OnBehalfOf *transport.Delegation
	// SLS names the orchestration files, as `state.orchestrate` takes
	// them.
	SLS []string
	Env string
	// Pillar is the override the run is compiled with, which is how a
	// deployment passes a version to its steps.
	Pillar *value.Map
	Test   bool
	// ResumeOf and ResumeFrom pick a previous run up at a named step.
	ResumeOf   job.ID
	ResumeFrom string
	// Plan is issue 765's signed plan, when the run has one: every fleet
	// step is sent as the plan's signed job, and the run is refused
	// before any step if the plan and this hub's compile disagree.
	// PlanDigest identifies the plan's bytes, for the record and the log.
	Plan       *orchplan.Plan
	PlanDigest string

	// started, when set, is called once the run is recorded and about to
	// run its first step, with its jid. An `async` runner call returns
	// then (startOrchestration); a compile error, a refused plan or a bad
	// resume all come before it, so they are still the call's answer.
	started func(job.ID)
}

// orchRunner carries what the step modules need, for one run.
//
// The step modules are built per run rather than registered once,
// because each closes over the principal the run is authorized as. A
// registry shared between two runs would have to be told whose it is on
// every call, and the version of that mistake is a step running with
// someone else's permissions.
type orchRunner struct {
	server     *Server
	principal  string
	onBehalfOf *transport.Delegation
	env        string
	jid        job.ID
	// plan is the run's signed plan, or nil for an unsigned run.
	plan *orchplan.Plan

	mu      sync.Mutex
	details map[string]*OrchStep

	// run is the record being kept, and sofar and chunks what checkpoint
	// builds its steps from. Nil run is a runner built for something
	// other than a run (orchestrate_show_sls), which records nothing.
	run    *OrchRun
	sofar  []*runner.StateResult
	chunks map[string]*state.Chunk
}

// detail returns the record a step writes its per-node outcome into.
func (o *orchRunner) detail(id string) *OrchStep {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.details == nil {
		o.details = map[string]*OrchStep{}
	}
	d, ok := o.details[id]
	if !ok {
		d = &OrchStep{ID: id}
		o.details[id] = d
	}
	return d
}

// Orchestrate compiles an orchestration SLS on the hub and runs it.
//
// The state compiler and the state runner are the node's, unchanged:
// an orchestration is a state run whose modules act on the fleet rather
// than on the local machine, and writing a second compiler for it would
// mean two implementations of `require`, `onfail`, and ordering that
// have to agree.
func (s *Server) Orchestrate(ctx context.Context, req OrchRequest) (*OrchRun, error) {
	if s.Files == nil {
		return nil, fmt.Errorf("this hub serves no tree, so it has no orchestration to compile")
	}
	env := req.Env
	if env == "" {
		env = "base"
	}
	if len(req.SLS) == 0 {
		return nil, fmt.Errorf("an orchestration needs an SLS to run")
	}

	jid := s.clock().Next()
	run := &OrchRun{
		JID:        jid,
		SLS:        req.SLS,
		Env:        env,
		Principal:  req.Principal,
		OnBehalfOf: actingFor(req.OnBehalfOf),
		Started:    s.now(),
		State:      OrchRunning,
		Test:       req.Test,
		// What the run compiled with, for a resume to be held to.
		PillarDigest: pillarDigest(req.Pillar),
		PlanDigest:   req.PlanDigest,
	}

	orch := &orchRunner{server: s, principal: req.Principal, onBehalfOf: req.OnBehalfOf, env: env, jid: jid,
		plan: req.Plan}
	registry := orchStates(orch)

	compileStarted := s.now()
	compiled := s.compileOrchestration(req, env, jid, registry)
	compileTook := s.now().Sub(compileStarted)
	for _, w := range compiled.RenderWarnings {
		s.warn(w.String(), "component", "orchestration", "jid", string(jid))
	}
	for _, d := range compiled.Diags.Warnings() {
		s.warn(d.String(), "component", "orchestration", "jid", string(jid))
	}
	if err := compiled.Err(); err != nil {
		// Counted, and timed: an orchestration that fails to compile
		// is one nobody ran, and it is the compilation that got slower
		// before it stopped working.
		s.countOrchestration("compile_failed", compileTook)
		return nil, err
	}

	seed, err := s.resumeSeed(req, compiled, run, orch)
	if err != nil {
		return nil, err
	}
	if req.Plan != nil {
		carried := make(map[string]bool, len(seed))
		for id := range seed {
			carried[id] = true
		}
		if err := s.checkPlan(req, env, compiled, carried); err != nil {
			kv := []any{"component", "orchestration", "jid", string(jid), "plan_digest", req.PlanDigest,
				"error", err.Error()}
			var refusal *planRefusal
			if errors.As(err, &refusal) {
				kv = append(kv, "step", refusal.step, "field", refusal.field)
			}
			s.warn("orchestration refused before it started", kv...)
			s.countOrchestration("plan_refused", compileTook)
			return nil, fmt.Errorf("orchestration %s refused before it started: %w", jid, err)
		}
		s.info("orchestration plan accepted", "component", "orchestration", "jid", string(jid),
			"plan_digest", req.PlanDigest, "signed_steps", len(req.Plan.Steps),
			"expires_in", planExpiresIn(req.Plan, s.now()).String())
	}

	newOrch := map[string]any{
		"jid": string(jid), "sls": req.SLS, "principal": req.Principal, "steps": len(compiled.Low),
	}
	if req.OnBehalfOf != nil {
		newOrch["on_behalf_of"] = req.OnBehalfOf.Principal
	}
	s.emit(tagOrchNew(string(jid)), "", newOrch)
	orch.run = run
	orch.chunks = make(map[string]*state.Chunk, len(compiled.Low))
	for _, ch := range compiled.Low {
		orch.chunks[ch.ID] = ch
	}
	s.recordOrch(run)
	// From here the run has a record `state.orch_show` can read, and
	// nothing that could still refuse it before a step: this is the
	// moment an asynchronous caller can be given the jid.
	if req.started != nil {
		req.started(jid)
	}

	result := (&runner.Runner{
		States: registry,
		// No execution registry: SPEC 25.5 keeps `cmd.*`, the file
		// write functions, and `module.run` off the hub entirely, and
		// the universal `unless`/`onlyif` gates reach the execution
		// registry to decide. An orchestration expresses a condition
		// with a requisite instead.
		Exec:     nil,
		Ctx:      s.orchContext(req, env, jid),
		FailHard: false,
		Seed:     seed,
		OnResult: orch.resulted,
	}).Run(compiled.Low)

	run.Steps = orch.timeline(result.Results)
	run.DurationMS = result.Duration.Milliseconds()
	run.State = OrchComplete
	// The verdict is over the steps this run actually ran. A carried
	// forward failure is why the operator resumed; reporting the
	// resumed run as failed because of it would mean a run that fixed
	// everything still says it failed, and no resume could ever
	// succeed.
	for _, sr := range result.Results {
		if _, carried := seed[sr.Chunk.ID]; carried {
			continue
		}
		if sr.Result.Failed() {
			run.State = OrchFailed
			break
		}
	}
	s.recordOrch(run)
	s.countOrchestration(string(run.State), compileTook)
	s.emit(tagOrchRet(string(jid)), "", map[string]any{
		"jid": string(jid), "state": run.State, "steps": len(run.Steps),
	})
	return run, nil
}

// compileOrchestration builds the low state for a run.
//
// A planned run compiles as the plan tool did (CompilePlanned): no hub
// configuration, no nodegroups, and a fixed seed in place of the job id,
// so that the only input that can differ between the two compiles is the
// tree.
func (s *Server) compileOrchestration(req OrchRequest, env string, jid job.ID, registry *states.Registry) *state.Compiled {
	in := orchCompileInputs{
		loader:     s.Files,
		config:     s.configValues(),
		nodegroups: s.nodegroups(),
		jobID:      string(jid),
		// What each orchestration file rendered to, at debug level,
		// as a node logs its states'. DIVERGENCE 5.261.
		onRendered: func(file, sls string, pipeline []string, text string) {
			if s.Log != nil {
				s.Log.Debug("rendered", "file", file, "sls", sls,
					"pipeline", strings.Join(pipeline, "|"), "rendered", text)
			}
		},
	}
	if req.Plan != nil {
		in.config, in.configRefused, in.nodegroups, in.jobID = nil, plannedConfigRefusal, nil, plannedJobID
	}
	return compileOrch(in, req.SLS, env, req.Pillar, req.Test, registry.Signatures())
}

// refuseParallelStateRuns refuses, at compile time, a step that asks for
// a state run to share a node.
//
// A state run always runs alone on the node (SPEC 9.6), so `run_parallel:
// true` on `salt.state`, `salt.sls` or `salt.highstate` -- or on a
// `salt.function` step whose function is a state run -- asks for something
// the job will not get. Accepting it and sending the flag would be the
// accept-but-ignore defect: the author reads an orchestration that
// overlaps a highstate with other work and gets one that waits. Refusing
// it where it is written is what lets them find out before a deployment
// rather than from its timing. `run_parallel: false` says what will happen
// anyway and is allowed, so a value templated across every step compiles.
//
// The truth of the value is read through the step's own signature, so
// `"no"` here means what it means to the step; a value the signature
// cannot read is already an error from the compiler's own check.
func refuseParallelStateRuns(compiled *state.Compiled, sigs *signature.Registry) {
	for _, ch := range compiled.Low {
		if ch.Args == nil || !ch.Args.Has("run_parallel") {
			continue
		}
		sig, ok := sigs.Lookup(ch.Func())
		if !ok {
			continue
		}
		bound, errs := sig.Bind(nil, ch.Args)
		if len(errs) > 0 {
			continue
		}
		if asked, _ := bound.Get("run_parallel"); asked != true {
			continue
		}
		fun := "state.apply"
		if ch.Func() == "salt.function" {
			fun = ch.Name
			if name, ok := bound.Get("name"); ok {
				fun = value.KeyString(name)
			}
		}
		if !job.AlwaysAlone(fun) {
			continue
		}
		compiled.Diags.Add(ch.Pos, ch.SLS, ch.ID,
			"%s: `run_parallel: true` is refused: the step sends %s, and a state run "+
				"always runs alone on a node (SPEC section 9.6), so it would wait for "+
				"other jobs whatever the step says; remove it, or set it to false",
			ch.Func(), fun)
	}
}

// OrchNodeID is what an orchestration template sees as `id`. It is the
// hub, because that is where the run happens; a step names its own
// targets.
const OrchNodeID = "hub"

// configValues is the hub's configuration as a template sees it, or an
// empty mapping for a hub assembled without one.
func (s *Server) configValues() *value.Map {
	if s.Pillar != nil && s.Pillar.ConfigValues != nil {
		return s.Pillar.ConfigValues
	}
	return value.NewMap(0)
}

// orchContext is the module context a step runs under.
func (s *Server) orchContext(req OrchRequest, env string, jid job.ID) *exec.Context {
	return &exec.Context{
		Ctx:    s.batchContext(),
		Grains: value.NewMap(0),
		Pillar: value.NewMap(0),
		Config: s.configValues(),
		NodeID: OrchNodeID,
		Env:    env,
		JobID:  string(jid),
		Test:   req.Test,
		Log: func(level, msg string) {
			s.info(msg, "component", "orchestration", "jid", string(jid), "level", level)
		},
	}
}

// resumeSeed builds the results a resumed run presents to requisites.
func (s *Server) resumeSeed(req OrchRequest, compiled *state.Compiled, run *OrchRun, orch *orchRunner) (map[string]states.Result, error) {
	if req.ResumeOf == "" {
		return nil, nil
	}
	if req.ResumeFrom == "" {
		return nil, fmt.Errorf("resuming needs the step to pick up at")
	}
	previous, err := s.orchStore().Get(req.ResumeOf)
	if err != nil {
		return nil, err
	}

	// The step named has to exist in the compilation being resumed,
	// or the run would start from the beginning without saying so.
	found := false
	for _, ch := range compiled.Low {
		if ch.ID == req.ResumeFrom {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("%s has no step %q; `orch show %s` lists them",
			req.ResumeOf, req.ResumeFrom, req.ResumeOf)
	}

	byID := map[string]*OrchStep{}
	for _, step := range previous.Steps {
		byID[step.ID] = step
	}

	// The override the steps were compiled with is part of what they
	// were. A resume compiled without it -- `orch resume` took none, and
	// the record kept none -- ran the remaining steps against a different
	// pillar from the one the carried-forward steps saw: a deploy started
	// with `version: 1.4.2` resumed with no version at all. So the
	// resumed run must be given the same override, and is refused,
	// saying which way it differs, when it is not. A record from before
	// runs kept the digest cannot say, and is resumed as it always was.
	if want := previous.PillarDigest; want != "" {
		got := pillarDigest(req.Pillar)
		switch {
		case got == want:
		case want == orchNoPillar:
			return nil, fmt.Errorf("%s ran with no pillar override and this resume passes one; "+
				"resume it without --pillar", req.ResumeOf)
		case got == orchNoPillar:
			return nil, fmt.Errorf("%s ran with a pillar override and this resume passes none; "+
				"pass the same --pillar it was started with", req.ResumeOf)
		default:
			return nil, fmt.Errorf("%s ran with a different pillar override from the one this resume "+
				"passes; pass the same --pillar it was started with", req.ResumeOf)
		}
	}

	// A dry run's steps did not happen, so a real run cannot be told
	// they did. Carrying them forward was exactly that: the predicted
	// results satisfied the requisites of the steps after the resume
	// point, and those were dispatched for real against machines the
	// earlier steps had never touched -- a deploy after a drain that
	// was only ever predicted. A dry resume of a dry run, and a dry
	// resume of a real one, claim nothing that did not happen and are
	// allowed.
	if !req.Test {
		if previous.Test {
			return nil, fmt.Errorf("%s was a test run, so none of its steps happened and there is nothing to "+
				"carry forward; run it for real, or resume it with --test", req.ResumeOf)
		}
		// A record written before runs recorded `test` cannot say. A
		// predicted change is the one result a real run never records
		// -- a real step succeeded or failed -- so a carried step whose
		// result is neither marks the run as a dry one. A dry run whose
		// every step predicted no change records only successes and is
		// not caught here; only the `test` field catches that.
		for _, ch := range compiled.Low {
			if ch.ID == req.ResumeFrom {
				break
			}
			if step, ran := byID[ch.ID]; ran && step.Result == nil && !step.Skipped && !step.Pending {
				return nil, fmt.Errorf("%s recorded step %q as a predicted change, which only a test run records, "+
					"so its steps did not happen; run it for real, or resume it with --test", req.ResumeOf, ch.ID)
			}
		}
	}

	seed := map[string]states.Result{}
	for _, ch := range compiled.Low {
		if ch.ID == req.ResumeFrom {
			break
		}
		step, ran := byID[ch.ID]
		if !ran {
			// A step the earlier run never reached is not seeded: it
			// runs now, which is what "everything up to the named step
			// already happened" cannot claim about a step that did not.
			continue
		}
		if step.Pending {
			result, err := s.inFlightOutcome(orch, req.ResumeOf, ch, step)
			if err != nil {
				return nil, err
			}
			seed[ch.ID] = result
			continue
		}
		seed[ch.ID] = states.Result{
			Result:  step.Result,
			Comment: fmt.Sprintf("Carried forward from %s: %s", req.ResumeOf, step.Comment),
		}
	}
	run.ResumedFrom = req.ResumeFrom
	run.ResumedOf = req.ResumeOf
	return seed, nil
}

// inFlightOutcome is the result of a step the earlier run had dispatched
// and had no outcome for when its record was last written: the hub stopped
// while the step's job was out.
//
// The job may well have run. The node does not know the hub stopped; it
// runs the job and retries the return for about ten seconds
// (nodelink.PostReturn), so a hub back within that has it. So whether it
// ran is read from the job record, which is the hub's own account of what
// came back, and not guessed in either direction: guessing "no" runs a
// `cmd.run` twice, and guessing "yes" tells the steps after it that a
// database exists that nobody created. A job every node answered is
// carried forward with the outcome those returns make, exactly as the
// step would have made it; one that is still short of a node is refused,
// naming the job, because nothing on the hub can say what that node did.
func (s *Server) inFlightOutcome(orch *orchRunner, of job.ID, ch *state.Chunk, step *OrchStep) (states.Result, error) {
	unknown := func(why string) error {
		return fmt.Errorf("step %q of %s was in flight when its hub stopped, as job %s, and %s; "+
			"`halite-hub jobs show %s` says what the nodes returned. Resume --from %s to run it again, "+
			"or wait for the job and resume again", ch.ID, of, step.JID, why, step.JID, ch.ID)
	}
	if step.JID == "" {
		// Stopped between writing that the step was dispatching and
		// learning the jid: the job may have gone or not, and nothing
		// links it to this step to look it up by.
		return states.Result{}, fmt.Errorf("step %q of %s was being dispatched when its hub stopped, so "+
			"whether its job reached a node is not recorded; `halite-hub jobs list` shows the jobs sent "+
			"around %s. Resume --from %s to run it again", ch.ID, of,
			step.Started.UTC().Format(time.RFC3339), ch.ID)
	}
	if s.Jobs == nil || !job.ID(step.JID).Valid() {
		return states.Result{}, unknown("this hub has no record of that job")
	}
	id := job.ID(step.JID)
	j, err := s.Jobs.Get(id)
	if err != nil {
		return states.Result{}, unknown(fmt.Sprintf("its record cannot be read (%v)", err))
	}
	missing, err := s.Jobs.Missing(id)
	if err != nil {
		return states.Result{}, unknown(fmt.Sprintf("its record cannot be read (%v)", err))
	}
	if j.State == job.Batching {
		return states.Result{}, unknown("it is still sending its batches")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return states.Result{}, unknown(fmt.Sprintf("%d of %d node(s) have not returned (%s)",
			len(missing), len(j.Nodes), strings.Join(missing, ", ")))
	}
	returns, err := s.Jobs.Returns(id)
	if err != nil {
		return states.Result{}, unknown(fmt.Sprintf("its returns cannot be read (%v)", err))
	}
	res := stepOutcome(orch, ch.ID, j, returns, nil, tolerated(ch.Args))
	res.Comment = fmt.Sprintf("Carried forward from %s, where it was in flight when the hub stopped; "+
		"job %s has since been answered by every node: %s", of, step.JID, res.Comment)
	return res, nil
}

// resulted is the runner's OnResult: it keeps what the run has so far, and
// writes the record when a step has run.
//
// Only then, because a step seeded by a resume, skipped by a requisite or
// passed over by an abort changed nothing, and a resume that does not find
// it recorded evaluates it again and comes to the same answer. That keeps
// the writes to one per step that did something -- plus one when a step
// dispatches its job (dispatched) -- rather than one per step, which for a
// run that fails early is the rest of the run skipped in a burst.
func (o *orchRunner) resulted(sofar []*runner.StateResult) {
	o.sofar = sofar
	if last := sofar[len(sofar)-1]; !last.Skipped {
		o.checkpoint()
	}
}

// dispatching records that a step is about to send its job, before it
// has a jid.
//
// The job is on its way to the nodes before dispatchFor returns -- a node
// can be running it before the hub has written a word about it -- so a
// record that named the step only once it had the jid would leave a gap
// in which a hub that stopped had sent a job its record does not mention,
// and a resume past it would send it again. Written first, the gap is the
// other way round: the record names a step whose job may or may not have
// gone, and a resume past it is refused (inFlightOutcome), which is a
// question for the operator rather than a second `cmd.run`. The jid
// cannot be had first: a hub chooses it inside the dispatch, and only a
// signed job brings its own.
func (o *orchRunner) dispatching(stepID string) {
	d := o.detail(stepID)
	o.mu.Lock()
	d.JID = ""
	d.Nodes = nil
	d.Pending = true
	d.Started = o.server.now()
	o.mu.Unlock()
	o.checkpoint()
}

// dispatched records that a step has sent its job and is waiting for it.
//
// Written before the wait, because the wait is where a long step spends
// its time, and so where the hub is most likely to stop: a record that
// learnt of the job only when it was answered would, for a hub killed
// during a two-hour migration, say nothing about the migration at all.
func (o *orchRunner) dispatched(stepID string, j *job.Job) {
	d := o.detail(stepID)
	o.mu.Lock()
	d.JID = string(j.JID)
	d.Nodes = j.Nodes
	d.Pending = true
	o.mu.Unlock()
	o.checkpoint()
}

// checkpoint writes the run as it stands: every step that has a result,
// and after them any step that has dispatched its job and has none yet.
//
// The whole record each time, as the start and the end always wrote it,
// rather than a journal of steps: `orch show`, `orch list` and a resume
// each read one file, and a second format for a run still going is a second
// reader that has to agree with the first. The cost is a write of the
// timeline so far per step, which against a step that waits on the fleet
// is small, and is atomic (writeAtomic), so a hub killed mid-write leaves
// the previous version rather than half of this one. Every call is on the
// goroutine running the steps, so two checkpoints never race; the run
// itself is copied rather than given Steps, so that the struct Orchestrate
// returns is only ever assembled once, at the end.
func (o *orchRunner) checkpoint() {
	if o.run == nil {
		return
	}
	snapshot := *o.run
	snapshot.Steps = o.timeline(o.sofar)

	have := make(map[string]bool, len(snapshot.Steps))
	for _, step := range snapshot.Steps {
		have[step.ID] = true
	}
	o.mu.Lock()
	var waiting []*OrchStep
	for id, d := range o.details {
		if !d.Pending || have[id] {
			continue
		}
		step := *d
		if ch, ok := o.chunks[id]; ok {
			step.Fun = ch.Func()
			step.SLS = ch.SLS
		}
		step.Order = len(snapshot.Steps)
		step.Comment = fmt.Sprintf("Dispatched as job %s; no outcome is recorded yet.", d.JID)
		if d.JID == "" {
			step.Comment = "Dispatching its job; no outcome is recorded yet."
		}
		waiting = append(waiting, &step)
	}
	o.mu.Unlock()
	sort.Slice(waiting, func(i, k int) bool { return waiting[i].ID < waiting[k].ID })
	snapshot.Steps = append(snapshot.Steps, waiting...)
	o.server.recordOrch(&snapshot)
}

// timeline assembles the record from the run's results and the per-node
// detail each step recorded.
func (o *orchRunner) timeline(results []*runner.StateResult) []*OrchStep {
	o.mu.Lock()
	defer o.mu.Unlock()

	out := make([]*OrchStep, 0, len(results))
	for _, sr := range results {
		step := &OrchStep{}
		if d, ok := o.details[sr.Chunk.ID]; ok {
			*step = *d
		}
		step.ID = sr.Chunk.ID
		// It has a result, so it is not waiting any more, whatever the
		// detail it dispatched with said.
		step.Pending = false
		step.Fun = sr.Chunk.Func()
		step.SLS = sr.Chunk.SLS
		step.Order = sr.RunNum
		step.Result = sr.Result.Result
		step.Comment = sr.Result.Comment
		step.Skipped = sr.Skipped
		step.Started = sr.StartTime
		step.DurationMS = sr.Duration.Milliseconds()
		if sr.Result.Changes != nil && sr.Result.Changes.Len() > 0 {
			if raw, err := value.EncodeJSON(sr.Result.Changes, 0); err == nil {
				step.Changes = raw
			}
		}
		out = append(out, step)
	}
	return out
}

// orchStore is the record store, or an empty one that reports it keeps
// nothing rather than pretending a run was saved.
func (s *Server) orchStore() *OrchStore {
	s.orchOnce.Do(func() {
		if s.Orch == nil {
			s.Orch = &OrchStore{}
		}
	})
	return s.Orch
}

func (s *Server) recordOrch(run *OrchRun) {
	if err := s.orchStore().Put(run); err != nil {
		s.warn("could not record an orchestration", "jid", string(run.JID), "error", err.Error())
	}
}

// awaitReturns waits for a dispatched job to be answered.
//
// Polling the cache rather than waiting on a channel, for the reason
// SPEC 9.3 gives for batching: the job and its returns are hub-side
// records, so a step that is retried, or a hub that restarts, sees the
// same thing the first attempt saw.
func (s *Server) awaitReturns(ctx context.Context, id job.ID, timeout time.Duration) (*job.Job, []*job.Return, []string, error) {
	if s.Jobs == nil {
		return nil, nil, nil, fmt.Errorf("this hub keeps no job cache, so a step cannot be waited for")
	}
	collect := func() (*job.Job, []*job.Return, []string, error) {
		j, err := s.Jobs.Get(id)
		if err != nil {
			return nil, nil, nil, err
		}
		missing, err := s.Jobs.Missing(id)
		if err != nil {
			return nil, nil, nil, err
		}
		returns, err := s.Jobs.Returns(id)
		if err != nil {
			return nil, nil, nil, err
		}
		sort.Strings(missing)
		return j, returns, missing, nil
	}

	deadline := s.now().Add(timeout)
	for {
		j, returns, missing, err := collect()
		if err != nil {
			return nil, nil, nil, err
		}
		if (len(missing) == 0 && j.State != job.Batching) || s.now().After(deadline) {
			return j, returns, missing, nil
		}
		select {
		case <-ctx.Done():
			// A deadline is the step's own timeout, and what has
			// arrived by then is the answer: some nodes returned and
			// some did not, which is a result rather than an error.
			// Only a real cancellation -- the hub stopping -- is one.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return collect()
			}
			return nil, nil, nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// stepOutcome turns a dispatched job's returns into a step result.
func stepOutcome(o *orchRunner, id string, j *job.Job, returns []*job.Return, missing []string, tolerate []string) states.Result {
	detail := o.detail(id)
	detail.JID = string(j.JID)
	detail.Nodes = j.Nodes
	detail.Missing = missing
	detail.Returns = map[string]json.RawMessage{}

	var failed []string
	changed := value.NewMap(len(returns))
	for _, ret := range returns {
		detail.Returns[ret.NodeID] = ret.Return
		changed.Set(ret.NodeID, decodedReturn(ret))
		if !ret.Success {
			failed = append(failed, ret.NodeID)
		}
	}
	sort.Strings(failed)
	detail.Failed = failed

	// A node named by `tolerate_failures` may fail without failing the
	// step, which is what an estate with a known-broken machine needs
	// in order to deploy to the rest of it.
	blocking := excludeTolerated(failed, tolerate)
	unanswered := excludeTolerated(missing, tolerate)

	switch {
	case len(j.Nodes) == 0:
		return states.False("No node matched " + j.Target + ".")
	case len(blocking) > 0 && len(unanswered) > 0:
		return states.Result{
			Result: boolPtr(false), Changes: changed,
			Comment: fmt.Sprintf("%s: %d of %d node(s) failed (%s) and %d never answered (%s).",
				j.Fun, len(blocking), len(j.Nodes), strings.Join(blocking, ", "),
				len(unanswered), strings.Join(unanswered, ", ")),
		}
	case len(blocking) > 0:
		return states.Result{
			Result: boolPtr(false), Changes: changed,
			Comment: fmt.Sprintf("%s: %d of %d node(s) failed: %s.",
				j.Fun, len(blocking), len(j.Nodes), strings.Join(blocking, ", ")),
		}
	case len(unanswered) > 0:
		// Distinct from a failure, and reported as one: nothing said
		// no, and something did not say anything.
		return states.Result{
			Result: boolPtr(false), Changes: changed,
			Comment: fmt.Sprintf("%s: %d of %d node(s) never answered: %s.",
				j.Fun, len(unanswered), len(j.Nodes), strings.Join(unanswered, ", ")),
		}
	}
	return states.Result{
		Result: boolPtr(true), Changes: changed,
		Comment: fmt.Sprintf("%s ran on %d node(s).", j.Fun, len(j.Nodes)),
	}
}

// excludeTolerated drops the nodes a step was told it may lose.
func excludeTolerated(nodes, tolerate []string) []string {
	if len(tolerate) == 0 || len(nodes) == 0 {
		return nodes
	}
	var out []string
	for _, id := range nodes {
		if toleratesNode(tolerate, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// toleratesNode matches a node against the globs a step was given, which are
// Salt's globs: fnmatch, as `fail_minions` takes them. They were path.Match,
// whose `[!1]` is the set `!` and `1`: `tolerate_failures: ['web[!1]*']`, meant
// to forgive every web node but the canary, forgave only the canary and
// failed the step for the rest. DIVERGENCE 5.502.
func toleratesNode(patterns []string, id string) bool {
	for _, p := range patterns {
		if p == id || fnmatch.Match(p, id) {
			return true
		}
	}
	return false
}

func boolPtr(b bool) *bool { return &b }

// orchNoPillar is PillarDigest for a run with no override, so that a
// record which had none is told apart from one that predates the field.
const orchNoPillar = "none"

// pillarDigest identifies an override by its content, independent of
// the order its keys were written in: the same override typed twice
// with keys in a different order is the same override.
func pillarDigest(p *value.Map) string {
	if p == nil || p.Len() == 0 {
		return orchNoPillar
	}
	encoded, err := json.Marshal(plainForDigest(p))
	if err != nil {
		// Unreachable for a mapping that came from JSON; and a value
		// that cannot be encoded cannot be matched, so it is its own
		// digest and no resume will match it by accident.
		return "unencodable:" + err.Error()
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// plainForDigest turns the ordered model into Go maps, which
// encoding/json writes with sorted keys.
func plainForDigest(v any) any {
	switch t := v.(type) {
	case *value.Map:
		out := make(map[string]any, t.Len())
		for _, e := range t.Entries() {
			out[value.KeyString(e.Key)] = plainForDigest(e.Val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = plainForDigest(item)
		}
		return out
	}
	return v
}
