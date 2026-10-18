package policy

// Scope is the set of nodes a principal may reach through one runner.
//
// SPEC 23.5 grants runners by name, with no target, which is Salt's
// model: `spec_check` in salt/utils/minions.py consults no target for
// `@runner`, and a runner runs with the whole reach of Salt's server, so
// `pillar.show_pillar db1` answers for any node whatever the caller's
// other rules name (issue 321, M-56). halite is stricter. A runner rule
// may name a `target`, and a runner that reads or changes what the hub
// holds about a node reaches only the nodes the targets of the rules
// granting it cover. A rule with no target grants the runner and no
// nodes: a missing field is not a wildcard, as everywhere else in the
// policy. What each runner reaches is declared beside it in the hub.
type Scope struct {
	Targets []string
	// within narrows the scope further: a node must be inside both.
	within *Scope
}

// Covers reports whether one node falls inside the scope, by the same
// match a function rule's target makes of a node ID.
func (s Scope) Covers(node string) bool {
	if s.within != nil && !s.within.Covers(node) {
		return false
	}
	for _, t := range s.Targets {
		if literalCovered(t, node) {
			return true
		}
	}
	return false
}

// Within is the scope narrowed to another: a node inside both. It is how
// a runner called through the API is held to the operator's scope as well
// as the API's own, which can only make it smaller.
func (s Scope) Within(other Scope) Scope {
	o := other
	s.within = &o
	return s
}

// Everything reports whether the scope is the whole estate, written as
// a target of `*`. A runner that acts on every node at once -- clearing
// every cache, pruning every job -- needs that, written, rather than the
// union of narrower rules happening to cover today's fleet.
func (s Scope) Everything() bool {
	if s.within != nil && !s.within.Everything() {
		return false
	}
	for _, t := range s.Targets {
		if t == "*" {
			return true
		}
	}
	return false
}

// RunnerScope is the scope of fun for principal: the targets of every
// rule, in every role bound to the principal, that grants the runner,
// by the same test Authorize uses. neverWildcard is Authorize's: a
// runner that a wildcard never grants is in scope only through a rule
// that names it.
func (p *Policy) RunnerScope(principal, fun string, neverWildcard bool) Scope {
	if p == nil {
		return Scope{}
	}
	return p.RunnerScopeFor(p.RolesFor(principal), fun, neverWildcard)
}

// RunnerScopeFor is RunnerScope for a set of roles already resolved, as
// an API token carries them.
func (p *Policy) RunnerScopeFor(roles []string, fun string, neverWildcard bool) Scope {
	if p == nil {
		return Scope{}
	}
	var sc Scope
	seen := map[string]bool{}
	for _, role := range roles {
		for _, rule := range p.Roles[role] {
			if !matchAny(rule.Runners, fun) {
				continue
			}
			if neverWildcard && !namedExactly(rule.Runners, fun) {
				continue
			}
			if rule.Target == "" || seen[rule.Target] {
				continue
			}
			seen[rule.Target] = true
			sc.Targets = append(sc.Targets, rule.Target)
		}
	}
	return sc
}
