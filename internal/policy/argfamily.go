package policy

import (
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/state"
)

// orchestrationRunners are the runners that take orchestration names as
// `sls`: the ones runnerNames reads, and the ones an argument rule written
// for any of them holds.
//
// The runner registry is the hub's, which this package cannot import, so
// the list is written here and internal/hub holds it to the registry:
// TestTheOrchestrationFamilyIsEveryRunnerThatTakesSLS fails if a runner
// takes `sls` and is not here, or is here and does not. `state.orch_resume`
// is not: it takes a job ID, and the names it runs are the earlier run's.
var orchestrationRunners = []string{
	"state.orch",
	"state.orchestrate",
	"state.orchestrate_show_sls",
}

// resumeRunner picks up an earlier orchestration run. It takes a job ID,
// not `sls`, so it is not one of orchestrationRunners; but what it runs is
// the earlier run's orchestration names, compiled again, so the argument
// rules of that family hold it too. Its names come from the run store, which
// this package cannot read: the hub gives them as Request.Resumes (issue 649,
// the item PR 657 left open in DIVERGENCE 5.586).
const resumeRunner = "state.orch_resume"

// OrchestrationRunners lists the runners that take orchestration names as
// `sls`, for the hub's test and for `policy show`.
func OrchestrationRunners() []string {
	return append([]string(nil), orchestrationRunners...)
}

// ArgumentFamily is the set of functions that take the same names as fun,
// sorted, or nil when fun is in no family.
//
// An argument rule is written under one function's name, and Rule.Args was
// read by the name of the function asked for. So `allow_sls` written under
// `state.apply`, in a rule granting `state.*`, held state.apply and nothing
// else: `state.sls db.secret`, `state.highstate`, `state.show_sls
// db.secret` were all allowed, as was `state.orch` under an `allow_mods`
// written for `state.orchestrate` (issue 621). Every one of those takes the
// names the rule was written to limit, and the rule's author had no way to
// know a sibling was a way round it.
//
// There are two families:
//
//   - The SLS family, a job's: every function the node answers through its
//     state compiler. Each compiles the SLS names given positionally, or
//     the highstate when there are none (cmd/halite-node runStateJob), so
//     each takes what allow_sls and deny_sls judge, and each takes the
//     keywords a state run reads. It is state.JobFunctions, the list the
//     node dispatches on, so a function the node starts answering joins
//     it without anyone remembering to.
//   - The orchestration family, a runner's: the runners that take `sls`,
//     and `state.orch_resume`, which runs the names an earlier run took.
//
// A runner request is in the second family only, and a job in the first
// only: Rule.Args is one map for both, and `state.orchestrate` the job
// function is not something any node answers.
func ArgumentFamily(fun string, runner bool) []string {
	var members []string
	if runner {
		members = append(OrchestrationRunners(), resumeRunner)
	} else {
		members = state.JobFunctions()
	}
	for _, m := range members {
		if m == fun {
			sort.Strings(members)
			return members
		}
	}
	return nil
}

// sourcedRule is one argument rule and the function it was written under.
type sourcedRule struct {
	written string
	rule    ArgRule
}

// argRulesFor is every argument rule in a rule that holds fun: the one
// written under fun's own name first, then those written under the rest of
// its family, in name order.
//
// All of them hold, not the most specific one. If the exact name's rule
// replaced its siblings', writing `deny_kwargs` under `state.sls` would lift
// `allow_sls` from state.sls, and that is the same surprise as the defect.
// An operator who wants different limits for different members writes them
// in different rules, each granting its own functions.
func argRulesFor(rule Rule, fun string, runner bool) []sourcedRule {
	var out []sourcedRule
	if own, ok := rule.Args[fun]; ok {
		out = append(out, sourcedRule{fun, own})
	}
	for _, member := range ArgumentFamily(fun, runner) {
		if member == fun {
			continue
		}
		if r, ok := rule.Args[member]; ok {
			out = append(out, sourcedRule{member, r})
		}
	}
	return out
}

// Unheld is one of a rule's grants and what it grants that no argument rule
// in the rule holds. `policy show` prints it so that an operator who wrote
// an argument rule can see what it does not reach (issue 621).
type Unheld struct {
	// Pattern is the entry as written in `functions` or `runners`.
	Pattern string
	Runner  bool
	// Names are the functions it grants that no argument rule holds, and
	// Granted is how many it grants in all.
	Names   []string
	Granted int
}

// UnheldGrants lists, for a rule, every grant with a function no argument
// rule in the rule holds. runnerCatalog is the runners the build ships,
// which this package cannot read for itself; a runner pattern is expanded
// against it, and a function pattern against the catalog UseCatalog gave.
//
// What a pattern "grants" is what Authorize would grant by it: a name
// written exactly grants itself, shipped or not, and a pattern grants
// only what wildcardGrants allows, never a pillar reader. So the count is
// the one the hub would act on, not every name the glob happens to match.
func (p *Policy) UnheldGrants(rule Rule, runnerCatalog []string) []Unheld {
	var out []Unheld
	collect := func(patterns []string, runner bool, catalog []string) {
		for _, pattern := range patterns {
			var granted []string
			if !strings.ContainsAny(pattern, "*?[") {
				granted = []string{pattern}
			} else {
				for _, name := range catalog {
					if !matchGlob(pattern, name) || ReadsPillar(name, runner) {
						continue
					}
					if !runner && !p.wildcardGrants(name) {
						continue
					}
					granted = append(granted, name)
				}
			}
			u := Unheld{Pattern: pattern, Runner: runner, Granted: len(granted)}
			for _, name := range granted {
				if len(argRulesFor(rule, name, runner)) == 0 {
					u.Names = append(u.Names, name)
				}
			}
			if len(u.Names) > 0 {
				out = append(out, u)
			}
		}
	}
	shipped := make([]string, 0, len(p.Shipped))
	for name := range p.Shipped {
		shipped = append(shipped, name)
	}
	sort.Strings(shipped)
	collect(rule.Functions, false, shipped)
	runners := append([]string(nil), runnerCatalog...)
	sort.Strings(runners)
	collect(rule.Runners, true, runners)
	return out
}
