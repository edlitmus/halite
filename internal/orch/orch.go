// Package orch runs ordered work across the fleet from the control plane:
// drain the load balancer, upgrade the backends, put it back. It is
// halite's state.orchestrate.
//
// An orchestration file is an ordinary SLS file whose states are
// `halite.run` steps. That is deliberate: it means requisite ordering,
// failure gating, and the universal creates/unless/onlyif gates are the
// same code that runs a highstate, rather than a second implementation
// with its own bugs. A step's "result" is what the targeted agents
// returned, so `require` between steps means "those hosts finished, and
// succeeded".
package orch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/engine"
	"github.com/edlitmus/halite/internal/modules"
	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/yamlite"
)

// StepFunction is the only state function an orchestration file may use.
const StepFunction = "halite.run"

// DefaultStepTimeout bounds how long one step waits for its agents.
const DefaultStepTimeout = 10 * time.Minute

// Dispatcher queues a job, as the control plane does for an operator.
type Dispatcher func(transport.DispatchRequest, string) (transport.DispatchResponse, error)

// JobReader reports what has come back for a job so far.
type JobReader func(jobID string) (transport.JobInfo, bool)

// Emitter raises an event about the orchestration's progress.
type Emitter func(tag string, data map[string]any)

// Runner executes one orchestration.
type Runner struct {
	Dispatch    Dispatcher
	Jobs        JobReader
	Emit        Emitter
	Log         *log.Logger
	StepTimeout time.Duration
	// By is the identity steps are dispatched under.
	By string
}

// StepOutcome is what one step did.
type StepOutcome struct {
	ID      string                `json:"id"`
	Ok      bool                  `json:"result"`
	Changed bool                  `json:"changed"`
	Comment string                `json:"comment"`
	JobID   string                `json:"job_id,omitempty"`
	Agents  []string              `json:"agents,omitempty"`
	Results []transport.JobResult `json:"results,omitempty"`
}

// Run executes the steps in requisite order and returns their outcomes.
func (r *Runner) Run(ctx context.Context, id string, states []sls.State) []StepOutcome {
	if r.StepTimeout <= 0 {
		r.StepTimeout = DefaultStepTimeout
	}
	collected := map[string]StepOutcome{}

	// The engine drives ordering and gating; each step is a "module" call
	// that dispatches and waits.
	lookup := func(name string) (modules.Func, bool) {
		if name != StepFunction {
			return nil, false
		}
		return func(c *modules.Ctx, stepID string, args map[string]any) modules.Result {
			outcome, result := r.step(ctx, id, stepID, args, c.Test)
			collected[stepID] = outcome
			return result
		}, true
	}

	r.Emit(fmt.Sprintf("halite/orch/%s/start", id), map[string]any{
		"orchestration": id, "steps": len(states),
	})

	results := engine.RunWith(&modules.Ctx{}, states, lookup)

	out := make([]StepOutcome, 0, len(results))
	failed := 0
	for _, res := range results {
		outcome, ran := collected[res.ID]
		if !ran {
			// The step never dispatched: a failed requisite, or a gate.
			outcome = StepOutcome{ID: res.ID}
		}
		outcome.Ok = res.Res.Ok
		outcome.Changed = res.Res.Changed
		outcome.Comment = res.Res.Comment
		if !outcome.Ok {
			failed++
		}
		out = append(out, outcome)
	}

	r.Emit(fmt.Sprintf("halite/orch/%s/done", id), map[string]any{
		"orchestration": id, "steps": len(out), "failed": failed,
	})
	return out
}

// step dispatches one step and waits for every targeted agent to answer.
func (r *Runner) step(
	ctx context.Context, orchID, stepID string, args map[string]any, test bool,
) (StepOutcome, modules.Result) {
	outcome := StepOutcome{ID: stepID}

	req, err := request(args, test)
	if err != nil {
		return outcome, modules.Result{Comment: err.Error()}
	}
	resp, err := r.Dispatch(req, r.By)
	if err != nil {
		return outcome, modules.Result{Comment: fmt.Sprintf("dispatch: %v", err)}
	}
	outcome.JobID = resp.JobID
	outcome.Agents = resp.Agents

	r.Log.Printf("orchestration %s: step %q dispatched %s to %d agent(s)",
		orchID, stepID, req.Kind, len(resp.Agents))
	r.Emit(fmt.Sprintf("halite/orch/%s/step/%s", orchID, stepID), map[string]any{
		"orchestration": orchID, "step": stepID,
		"job_id": resp.JobID, "agents": resp.Agents, "state": "dispatched",
	})

	if len(resp.Agents) == 0 {
		// An empty target is a failure, not a silent success: a step that
		// was supposed to drain the load balancer and reached no load
		// balancer must stop the orchestration.
		return outcome, modules.Result{Comment: fmt.Sprintf("no online agents matched %q", req.Target)}
	}

	info, complete := r.wait(ctx, resp)
	outcome.Results = info.Results

	changed, failures := 0, 0
	for _, res := range info.Results {
		if !res.Ok {
			failures++
		}
		changed += res.Changed
	}
	missing := len(resp.Agents) - len(info.Results)

	result := modules.Result{
		Ok:      complete && failures == 0,
		Changed: changed > 0,
	}
	switch {
	case !complete:
		result.Comment = fmt.Sprintf("timed out waiting for %d of %d agent(s)", missing, len(resp.Agents))
	case failures > 0:
		result.Comment = fmt.Sprintf("%d of %d agent(s) failed", failures, len(info.Results))
	default:
		result.Comment = fmt.Sprintf("%d agent(s) succeeded, %d changed", len(info.Results), changed)
	}

	r.Emit(fmt.Sprintf("halite/orch/%s/step/%s", orchID, stepID), map[string]any{
		"orchestration": orchID, "step": stepID, "job_id": resp.JobID,
		"state": "finished", "result": result.Ok, "comment": result.Comment,
	})
	return outcome, result
}

// wait polls until every targeted agent has answered or the step times out.
func (r *Runner) wait(ctx context.Context, resp transport.DispatchResponse) (transport.JobInfo, bool) {
	deadline := time.Now().Add(r.StepTimeout)
	for {
		info, known := r.Jobs(resp.JobID)
		if known && len(info.Results) >= len(resp.Agents) {
			return info, true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return info, false
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return info, false
		}
	}
}

// request turns a step's arguments into a dispatch.
func request(args map[string]any, test bool) (transport.DispatchRequest, error) {
	req := transport.DispatchRequest{
		Target: modules.Str(args, "target", ""),
		Kind:   modules.Str(args, "kind", transport.KindHighstate),
		Fn:     modules.Str(args, "fn", ""),
		SLS:    modules.List(args, "sls"),
		Test:   test || modules.Bool(args, "test", false),
	}
	if req.Target == "" {
		return req, fmt.Errorf("step needs a target")
	}
	if raw, ok := args["args"]; ok {
		parsed, err := stepArgs(raw)
		if err != nil {
			return req, err
		}
		req.Args = parsed
	}
	switch req.Kind {
	case transport.KindHighstate, transport.KindGrains, transport.KindPillar:
	case transport.KindApply:
		if len(req.SLS) == 0 {
			return req, fmt.Errorf("state.apply needs at least one sls name")
		}
	case transport.KindCall:
		if !strings.Contains(req.Fn, ".") {
			return req, fmt.Errorf("call needs a module.function")
		}
	default:
		return req, fmt.Errorf("unknown kind %q", req.Kind)
	}
	return req, nil
}

// stepArgs reads the `args:` mapping of a call step. The SLS compiler
// leaves nested values as parsed yamlite, so that is what arrives here.
func stepArgs(raw any) (map[string]string, error) {
	body, ok := raw.(*yamlite.Map)
	if !ok {
		return nil, fmt.Errorf("args: must be a mapping")
	}
	out := make(map[string]string, len(body.Keys))
	for _, key := range body.Keys {
		text, ok := body.Vals[key].(string)
		if !ok {
			return nil, fmt.Errorf("args: %s must be a scalar", key)
		}
		out[key] = text
	}
	return out, nil
}
