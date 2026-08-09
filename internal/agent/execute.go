package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/edlitmus/halite/internal/engine"
	"github.com/edlitmus/halite/internal/modules"
	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/transport"
)

// execute runs one job and builds its result. Failures become a failed
// result rather than an error: the operator who dispatched the job needs to
// hear why it did not run, and an agent that goes quiet on failure is worse
// than one that reports a problem.
func (a *Agent) execute(ctx context.Context, job transport.Job) transport.JobResult {
	started := time.Now()
	result := transport.JobResult{JobID: job.ID, AgentID: a.cfg.ID}
	a.log.Printf("job %s: %s (test=%v)", job.ID, job.Kind, job.Test)

	switch job.Kind {
	case transport.KindGrains:
		result.Ok = true
		result.Data = a.grains
	case transport.KindPillar:
		data, err := a.fetchPillar(ctx)
		if err != nil {
			result.Error = err.Error()
			break
		}
		result.Ok = true
		result.Data = data
	case transport.KindCall:
		result = a.runCall(ctx, job, result)
	case transport.KindHighstate, transport.KindApply:
		result = a.runStates(ctx, job, result)
	default:
		result.Error = fmt.Sprintf("unknown job kind %q", job.Kind)
	}

	result.Finished = time.Now().UTC()
	result.Duration = time.Since(started)
	if result.Error != "" {
		a.log.Printf("job %s: %s", job.ID, result.Error)
	}
	return result
}

// runStates fetches the tree and this agent's pillar, then runs the same
// loader and engine as `halite apply`.
func (a *Agent) runStates(ctx context.Context, job transport.Job, result transport.JobResult) transport.JobResult {
	pillarData, err := a.fetchPillar(ctx)
	if err != nil {
		result.Error = fmt.Sprintf("fetch pillar: %v", err)
		return result
	}
	root, err := a.fetchStateTree(ctx)
	if err != nil {
		result.Error = fmt.Sprintf("fetch state tree: %v", err)
		return result
	}

	loader := &sls.Loader{Root: root, Grains: a.grains, Pillar: pillarData}
	var states []sls.State
	if job.Kind == transport.KindHighstate {
		states, err = loader.LoadTop()
	} else {
		states, err = loader.LoadNames(job.SLS)
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}

	ctxModules := &modules.Ctx{Test: job.Test, Grains: a.grains, Pillar: pillarData}
	for _, r := range engine.Run(ctxModules, states) {
		result.States = append(result.States, transport.StateOutcome{
			ID:       r.ID,
			Function: r.Fn,
			Ok:       r.Res.Ok,
			Changed:  r.Res.Changed,
			Comment:  r.Res.Comment,
			Changes:  r.Res.Changes,
		})
		switch {
		case !r.Res.Ok:
			result.Failed++
		case r.Res.Changed:
			result.Succeeded++
			result.Changed++
		default:
			result.Succeeded++
		}
	}
	result.Ok = result.Failed == 0
	return result
}

// runCall runs a single state function ad hoc, the fleet-wide equivalent of
// `halite call`.
func (a *Agent) runCall(ctx context.Context, job transport.Job, result transport.JobResult) transport.JobResult {
	fn, ok := modules.Registry[job.Fn]
	if !ok {
		result.Error = fmt.Sprintf("unknown state function %q", job.Fn)
		return result
	}
	pillarData, err := a.fetchPillar(ctx)
	if err != nil {
		result.Error = fmt.Sprintf("fetch pillar: %v", err)
		return result
	}

	args := make(map[string]any, len(job.Args))
	for k, v := range job.Args {
		args[k] = v
	}
	res := modules.Result{Ok: true}
	if comment, gated := modules.CheckGates(args); gated {
		res.Comment = comment
	} else {
		res = fn(&modules.Ctx{Test: job.Test, Grains: a.grains, Pillar: pillarData}, job.Fn, args)
	}

	result.States = []transport.StateOutcome{{
		ID: job.Fn, Function: job.Fn, Ok: res.Ok,
		Changed: res.Changed, Comment: res.Comment, Changes: res.Changes,
	}}
	result.Ok = res.Ok
	if res.Ok {
		result.Succeeded = 1
		if res.Changed {
			result.Changed = 1
		}
	} else {
		result.Failed = 1
	}
	return result
}
