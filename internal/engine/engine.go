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

// Lookup resolves a state function by name. Run uses the module registry;
// the orchestrator supplies its own, so that requisite ordering, failure
// gating, and the universal gates behave identically whether a step runs
// locally or dispatches across the fleet.
type Lookup func(name string) (modules.Func, bool)

func registryLookup(name string) (modules.Func, bool) {
	fn, ok := modules.Registry[name]
	return fn, ok
}

// StateResult pairs a state with its outcome.
type StateResult struct {
	ID  string
	Fn  string
	Res modules.Result
}

// Run executes states in order against the module registry. ctx.BaseDir is
// overridden per state with the directory of its source SLS file so
// relative sources resolve.
func Run(ctx *modules.Ctx, states []sls.State) []StateResult {
	return RunWith(ctx, states, registryLookup)
}

// RunWith is Run with a caller-supplied function resolver.
func RunWith(ctx *modules.Ctx, states []sls.State, lookup Lookup) []StateResult {
	results := make([]modules.Result, len(states))
	executedThrough := -1

	// A reference can name several states — `names:` expands one
	// declaration into one state per name — so the answer is the whole
	// group's: it failed if any failed, and it changed if any changed.
	resultOf := func(r sls.Ref) (modules.Result, bool) {
		combined := modules.Result{Ok: true}
		found := false
		for i := 0; i <= executedThrough; i++ {
			if !states[i].Matches(r) {
				continue
			}
			found = true
			combined.Ok = combined.Ok && results[i].Ok
			combined.Changed = combined.Changed || results[i].Changed
			combined.Comment = results[i].Comment
		}
		return combined, found
	}
	findStates := func(r sls.Ref) []sls.State {
		var out []sls.State
		for _, s := range states {
			if s.Matches(r) {
				out = append(out, s)
			}
		}
		return out
	}
	// failedPrereqOf finds an already-run state that declared this one as
	// its prereq target and failed. A failed prereq blocks its target: if
	// draining the load balancer failed, the deploy must not proceed.
	failedPrereqOf := func(st sls.State) (sls.State, bool) {
		for i := 0; i <= executedThrough; i++ {
			if results[i].Ok {
				continue
			}
			for _, r := range states[i].Prereq {
				if st.Matches(r) {
					return states[i], true
				}
			}
		}
		return sls.State{}, false
	}

	// dryRun evaluates whether a state would make changes, without making
	// them (used by prereq).
	dryRun := func(st sls.State) modules.Result {
		testCtx := &modules.Ctx{Test: true, Grains: ctx.Grains, Pillar: ctx.Pillar,
			Mine: ctx.Mine, BaseDir: st.Dir}
		args := copyArgs(st.Args)
		if comment, gated := modules.CheckGates(args); gated {
			return modules.Result{Ok: true, Comment: comment}
		}
		fn, ok := lookup(st.Name())
		if !ok {
			return modules.Result{Ok: false, Comment: "state function not found"}
		}
		return fn(testCtx, st.ID, args)
	}

	origBase := ctx.BaseDir
	defer func() { ctx.BaseDir = origBase }()

	out := make([]StateResult, 0, len(states))
	for i, st := range states {
		// Set per state rather than carrying the previous state's directory
		// into one that has none.
		if st.Dir != "" {
			ctx.BaseDir = st.Dir
		} else {
			ctx.BaseDir = origBase
		}
		res := runOne(ctx, st, lookup, resultOf, findStates, failedPrereqOf, dryRun)
		results[i] = res
		executedThrough = i
		out = append(out, StateResult{ID: st.ID, Fn: st.Name(), Res: res})
	}
	return out
}

func runOne(
	ctx *modules.Ctx,
	st sls.State,
	lookup Lookup,
	resultOf func(sls.Ref) (modules.Result, bool),
	findStates func(sls.Ref) []sls.State,
	failedPrereqOf func(sls.State) (sls.State, bool),
	dryRun func(sls.State) modules.Result,
) modules.Result {
	// Any failed requisite blocks execution.
	after := append(append(append([]sls.Ref{}, st.Require...), st.Watch...), st.OnChanges...)
	for _, r := range after {
		if res, ok := resultOf(r); ok && !res.Ok {
			return modules.Result{Ok: false,
				Comment: fmt.Sprintf("one or more requisite failed: %s:%s", r.Module, r.ID)}
		}
	}
	if s, blocked := failedPrereqOf(st); blocked {
		return modules.Result{Ok: false,
			Comment: fmt.Sprintf("one or more requisite failed: %s:%s", s.Module, s.ID)}
	}

	// onchanges: run only if at least one referenced state changed.
	if len(st.OnChanges) > 0 {
		triggered := false
		for _, r := range st.OnChanges {
			if res, ok := resultOf(r); ok && res.Changed {
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
			for _, target := range findStates(r) {
				if pr := dryRun(target); pr.Ok && pr.Changed {
					would = true
					break
				}
			}
			if would {
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
		if res, ok := resultOf(r); ok && res.Changed {
			args["__watch_changed"] = "true"
			break
		}
	}
	if comment, gated := modules.CheckGates(args); gated {
		return modules.Result{Ok: true, Comment: comment}
	}
	fn, ok := lookup(st.Name())
	if !ok {
		return modules.Result{Ok: false,
			Comment: fmt.Sprintf("state function %q not found", st.Name())}
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
