package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/runner"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// OrchRequest is one orchestration to run.
type OrchRequest struct {
	Principal string
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
}

// orchRunner carries what the step modules need, for one run.
//
// The step modules are built per run rather than registered once,
// because each closes over the principal the run is authorized as. A
// registry shared between two runs would have to be told whose it is on
// every call, and the version of that mistake is a step running with
// someone else's permissions.
type orchRunner struct {
	server    *Server
	principal string
	env       string
	jid       job.ID

	mu      sync.Mutex
	details map[string]*OrchStep
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
		JID:       jid,
		SLS:       req.SLS,
		Env:       env,
		Principal: req.Principal,
		Started:   s.now(),
		State:     OrchRunning,
	}

	orch := &orchRunner{server: s, principal: req.Principal, env: env, jid: jid}
	registry := orchStates(orch)

	compiled := s.compileOrchestration(req, env, jid, registry)
	for _, w := range compiled.RenderWarnings {
		s.warn(w.String(), "component", "orchestration", "jid", string(jid))
	}
	for _, d := range compiled.Diags.Warnings() {
		s.warn(d.String(), "component", "orchestration", "jid", string(jid))
	}
	if err := compiled.Err(); err != nil {
		return nil, err
	}

	seed, err := s.resumeSeed(req, compiled, run)
	if err != nil {
		return nil, err
	}

	s.emit(tagOrchNew(string(jid)), "", map[string]any{
		"jid": string(jid), "sls": req.SLS, "principal": req.Principal, "steps": len(compiled.Low),
	})
	s.recordOrch(run)

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
	}).Run(compiled.Low)

	run.Steps = orch.timeline(compiled.Low, result)
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
	s.emit(tagOrchRet(string(jid)), "", map[string]any{
		"jid": string(jid), "state": run.State, "steps": len(run.Steps),
	})
	return run, nil
}

// compileOrchestration builds the low state for a run.
func (s *Server) compileOrchestration(req OrchRequest, env string, jid job.ID, registry *states.Registry) *state.Compiled {
	pillar := req.Pillar
	if pillar == nil {
		pillar = value.NewMap(0)
	}
	c := &state.Compiler{
		Loader:   s.Files,
		Registry: registry.Signatures(),
		Config: state.Config{
			Env:    env,
			JobID:  string(jid),
			NodeID: OrchNodeID,
			// The hub has no grains of its own to expose here, and
			// inventing a set would make an orchestration that reads
			// `grains` look portable when it is not.
			Grains:       value.NewMap(0),
			Pillar:       pillar,
			ConfigValues: s.configValues(),
			// SPEC 25.5: the hub's dispatcher is restricted, and this
			// build gives an orchestration template none at all rather
			// than one that has not been audited against that list.
			Salt:       nil,
			Undefined:  template.Strict,
			Nodegroups: s.nodegroups(),
			Test:       req.Test,
			GPG:        render.GPGOptions{},
		},
	}
	return c.CompileSLS(req.SLS)
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
func (s *Server) resumeSeed(req OrchRequest, compiled *state.Compiled, run *OrchRun) (map[string]states.Result, error) {
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
		seed[ch.ID] = states.Result{
			Result:  step.Result,
			Comment: fmt.Sprintf("Carried forward from %s: %s", req.ResumeOf, step.Comment),
		}
	}
	run.ResumedFrom = req.ResumeFrom
	run.ResumedOf = req.ResumeOf
	return seed, nil
}

// timeline assembles the record from the run's results and the per-node
// detail each step recorded.
func (o *orchRunner) timeline(chunks []*state.Chunk, result *runner.RunResult) []*OrchStep {
	o.mu.Lock()
	defer o.mu.Unlock()

	out := make([]*OrchStep, 0, len(result.Results))
	for _, sr := range result.Results {
		step := &OrchStep{}
		if d, ok := o.details[sr.Chunk.ID]; ok {
			*step = *d
		}
		step.ID = sr.Chunk.ID
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

// toleratesNode matches a node against the globs a step was given.
func toleratesNode(patterns []string, id string) bool {
	for _, p := range patterns {
		if p == id {
			return true
		}
		if ok, err := path.Match(p, id); err == nil && ok {
			return true
		}
	}
	return false
}

func boolPtr(b bool) *bool { return &b }
