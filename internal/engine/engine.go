// Package engine executes a compiled state plan: requisite gating, watch
// and onchanges propagation, prereq dry-runs, and universal creates/
// unless/onlyif gates. It is shared by the CLI today and the agent daemon
// in the planned transport phase.
package engine

import (
	"fmt"

	"github.com/edlitmus/halite/internal/modules"
	"github.com/edlitmus/halite/internal/sls"
)

// StateResult pairs a state with its outcome.
type StateResult struct {
	ID  string
	Fn  string
	Res modules.Result
}

// Run executes states in order. ctx.BaseDir is overridden per state with
// the directory of its source SLS file so relative sources resolve.
func Run(ctx *modules.Ctx, states []sls.State) []StateResult {
	results := make([]modules.Result, len(states))
	executedThrough := -1

	lookup := func(r sls.Ref) (modules.Result, bool) {
		for i := 0; i <= executedThrough; i++ {
			s := states[i]
			if s.ID == r.ID && (r.Module == "" || r.Module == s.Module) {
				return results[i], true
			}
		}
		return modules.Result{}, false
	}
	findState := func(r sls.Ref) (sls.State, bool) {
		for _, s := range states {
			if s.ID == r.ID && (r.Module == "" || r.Module == s.Module) {
				return s, true
			}
		}
		return sls.State{}, false
	}

	// dryRun evaluates whether a state would make changes, without making
	// them (used by prereq).
	dryRun := func(st sls.State) modules.Result {
		testCtx := &modules.Ctx{Test: true, Grains: ctx.Grains, BaseDir: st.Dir}
		args := copyArgs(st.Args)
		if comment, gated := modules.CheckGates(args); gated {
			return modules.Result{Ok: true, Comment: comment}
		}
		fn, ok := modules.Registry[st.Name()]
		if !ok {
			return modules.Result{Ok: false, Comment: "state function not found"}
		}
		return fn(testCtx, st.ID, args)
	}

	out := make([]StateResult, 0, len(states))
	for i, st := range states {
		res := runOne(ctx, st, lookup, findState, dryRun)
		results[i] = res
		executedThrough = i
		out = append(out, StateResult{ID: st.ID, Fn: st.Name(), Res: res})
	}
	return out
}

func runOne(
	ctx *modules.Ctx,
	st sls.State,
	lookup func(sls.Ref) (modules.Result, bool),
	findState func(sls.Ref) (sls.State, bool),
	dryRun func(sls.State) modules.Result,
) modules.Result {
	// Any failed requisite blocks execution.
	after := append(append(append([]sls.Ref{}, st.Require...), st.Watch...), st.OnChanges...)
	for _, r := range after {
		if res, ok := lookup(r); ok && !res.Ok {
			return modules.Result{Ok: false,
				Comment: fmt.Sprintf("one or more requisite failed: %s:%s", r.Module, r.ID)}
		}
	}

	// onchanges: run only if at least one referenced state changed.
	if len(st.OnChanges) > 0 {
		triggered := false
		for _, r := range st.OnChanges {
			if res, ok := lookup(r); ok && res.Changed {
				triggered = true
				break
			}
		}
		if !triggered {
			return modules.Result{Ok: true,
				Comment: "state not run: no changes in onchanges requisites"}
		}
	}

	// prereq: run only if at least one referenced (later) state would make
	// changes, determined by a dry run.
	if len(st.Prereq) > 0 {
		would := false
		for _, r := range st.Prereq {
			target, ok := findState(r)
			if !ok {
				continue // sort already validated; defensive
			}
			if pr := dryRun(target); pr.Ok && pr.Changed {
				would = true
				break
			}
		}
		if !would {
			return modules.Result{Ok: true,
				Comment: "state not run: prereq targets would make no changes"}
		}
	}

	args := copyArgs(st.Args)
	for _, r := range st.Watch {
		if res, ok := lookup(r); ok && res.Changed {
			args["__watch_changed"] = "true"
			break
		}
	}
	if comment, gated := modules.CheckGates(args); gated {
		return modules.Result{Ok: true, Comment: comment}
	}
	fn, ok := modules.Registry[st.Name()]
	if !ok {
		return modules.Result{Ok: false,
			Comment: fmt.Sprintf("state function %q not found", st.Name())}
	}
	if st.Dir != "" {
		ctx.BaseDir = st.Dir
	}
	return fn(ctx, st.ID, args)
}

func copyArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
