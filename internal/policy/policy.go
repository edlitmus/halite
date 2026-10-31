// Package policy is the RBAC of SPEC section 23.5: one file, one
// grammar, deny by default.
//
// Salt has publisher_acl, external_auth, peer, peer_run, and client_acl
// as five overlapping mechanisms with different syntaxes and different
// failure modes, which is why Salt authorization is so often wrong in
// practice. There is one mechanism here and a request must match one
// rule entirely -- target and function together, not separately.
package policy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/fnmatch"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// Policy is a loaded policy file.
type Policy struct {
	Roles    map[string][]Rule
	Bindings []Binding
	// Path is where it was read from, for a message that names it.
	Path string
	// ArbitraryCode is the set of functions a wildcard never grants.
	// The hub fills it in from the module signatures it ships, so a
	// function marked `arbitrary_code` in a later build is covered
	// without anyone remembering to add it here.
	ArbitraryCode map[string]bool
}

// Rule is one grant: a target pattern, the functions permitted against
// it, and any argument-level constraints.
type Rule struct {
	Target    string
	Functions []string
	Runners   []string
	Args      map[string]ArgRule
}

// ArgRule constrains the arguments of one function.
type ArgRule struct {
	AllowSLS   []string
	DenySLS    []string
	AllowMods  []string
	DenyKwargs []string
	// RequireKwargs names keywords a call must carry, each with exactly
	// the value given: `{test: true}` is a role that can run the function
	// only as a dry run. A keyword that is absent, or present with another
	// value or another type, refuses the call. Values are bool, string or
	// number; names are exact, never patterns. DIVERGENCE 5.493.
	RequireKwargs map[string]any
}

// Binding attaches roles to a principal.
type Binding struct {
	Principal string
	Roles     []string
}

// Request is what is being asked for.
type Request struct {
	Principal string
	Target    string
	// TargetKind is how Target is to be read, in the flag form dispatch
	// takes (target.KindFromFlag): "" or "glob", "L" or "list", "C" or
	// "compound", and so on.
	//
	// It is part of the question because the same string names
	// different nodes under different kinds. `web1.prod,db1.prod` is a
	// glob that matches nothing and a list that reaches db1.prod; a
	// policy that judged the string without the kind authorized the
	// first and dispatch then ran the second. Every site that
	// authorizes a job must pass the kind the job will be compiled
	// under -- the same value it hands to Dispatch -- or the two paths
	// disagree about which nodes were asked for. DIVERGENCE 5.296.
	TargetKind string
	Fun        string
	Arg        []string
	Kwarg      map[string]any
	// Test is the job's dry-run flag, which travels beside Kwarg and not
	// in it. On the wire it is the keyword `test` (job.WireKwargs sets it
	// from the flag, over whatever Kwarg says), so the policy judges the
	// keywords the node will see: a request that leaves it out has a
	// `test` the node never reads from Kwarg, and a role that requires
	// `test: true` would refuse every dry run. DIVERGENCE 5.493.
	Test bool
	// Runner marks a runner call rather than a fleet job, which is
	// granted by `runners:` rather than by `functions:`.
	Runner bool
	// NeverWildcard requires the function to be named in the role
	// rather than matched by a pattern, for the distinct
	// high-privilege permissions of SPEC 22.1. Reading one node's
	// compiled pillar is reading its secrets, and a role written to let
	// someone restart a service should not carry it because the list
	// said `*`.
	NeverWildcard bool
}

// Decision is the outcome, and why. SPEC 23.5: every decision is
// logged, allowed or denied, with the rule that matched.
type Decision struct {
	Allowed bool
	// Role and RuleIndex name what matched, for the log and for
	// `policy test`.
	Role      string
	RuleIndex int
	// Reason says why a request was denied, in terms an operator can
	// act on.
	Reason string
}

func (d Decision) String() string {
	if d.Allowed {
		return fmt.Sprintf("allowed by role %q rule %d", d.Role, d.RuleIndex)
	}
	return "denied: " + d.Reason
}

// Load reads a policy file.
//
// Warnings from the parser are returned rather than swallowed: a
// YAML 1.1 boolean in a policy file -- `functions: [on]` -- means
// something other than what it looks like, and this is not a file to
// find that out in later.
func Load(src []byte, path string) (*Policy, []yaml.Warning, error) {
	doc, warnings, err := yaml.Parse(src, yaml.Options{File: path})
	if err != nil {
		return nil, warnings, err
	}
	root, ok := doc.(*value.Map)
	if !ok {
		if doc == nil {
			// An empty file is a policy that grants nothing, which is
			// a legitimate thing to write and the safe default.
			return &Policy{Path: path, Roles: map[string][]Rule{}}, warnings, nil
		}
		return nil, warnings, fmt.Errorf("%s: a policy is a mapping with `roles` and `bindings`", path)
	}

	p := &Policy{Path: path, Roles: map[string][]Rule{}}
	if roles, ok := root.Get("roles"); ok {
		m, ok := roles.(*value.Map)
		if !ok {
			return nil, warnings, fmt.Errorf("%s: `roles` is a mapping of role name to rules", path)
		}
		for _, e := range m.Entries() {
			name := value.KeyString(e.Key)
			rules, err := parseRules(name, e.Val, path)
			if err != nil {
				return nil, warnings, err
			}
			p.Roles[name] = rules
		}
	}
	if bindings, ok := root.Get("bindings"); ok {
		list, ok := bindings.([]any)
		if !ok {
			return nil, warnings, fmt.Errorf("%s: `bindings` is a list", path)
		}
		for i, item := range list {
			m, ok := item.(*value.Map)
			if !ok {
				return nil, warnings, fmt.Errorf("%s: binding %d is not a mapping", path, i)
			}
			b := Binding{}
			if v, ok := m.Get("principal"); ok {
				b.Principal, _ = v.(string)
			}
			if b.Principal == "" {
				return nil, warnings, fmt.Errorf("%s: binding %d names no principal", path, i)
			}
			roles, err := stringList(m, "roles")
			if err != nil {
				return nil, warnings, fmt.Errorf("%s: binding for %q: %v", path, b.Principal, err)
			}
			b.Roles = roles
			for _, role := range b.Roles {
				if _, ok := p.Roles[role]; !ok {
					// A binding to a role that does not exist grants
					// nothing and looks like it grants something,
					// which is the worst combination.
					return nil, warnings, fmt.Errorf("%s: binding for %q names role %q, which is not defined",
						path, b.Principal, role)
				}
			}
			p.Bindings = append(p.Bindings, b)
		}
	}
	return p, warnings, nil
}

func parseRules(role string, v any, path string) ([]Rule, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: role %q is a list of rules", path, role)
	}
	var out []Rule
	for i, item := range list {
		m, ok := item.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("%s: role %q rule %d is not a mapping", path, role, i)
		}
		r := Rule{Args: map[string]ArgRule{}}
		if v, ok := m.Get("target"); ok {
			r.Target, _ = v.(string)
		}
		var err error
		if r.Functions, err = stringList(m, "functions"); err != nil {
			return nil, fmt.Errorf("%s: role %q rule %d: %v", path, role, i, err)
		}
		if r.Runners, err = stringList(m, "runners"); err != nil {
			return nil, fmt.Errorf("%s: role %q rule %d: %v", path, role, i, err)
		}
		if len(r.Functions) == 0 && len(r.Runners) == 0 {
			return nil, fmt.Errorf("%s: role %q rule %d grants no functions and no runners", path, role, i)
		}
		if len(r.Functions) > 0 && r.Target == "" {
			// SPEC 23.5: target and function are authorized together,
			// and a missing field is not a wildcard.
			return nil, fmt.Errorf("%s: role %q rule %d grants functions and names no target; write `target: '*'` to mean every node",
				path, role, i)
		}
		if args, ok := m.Get("args"); ok {
			am, ok := args.(*value.Map)
			if !ok {
				return nil, fmt.Errorf("%s: role %q rule %d: `args` is a mapping keyed by function", path, role, i)
			}
			for _, e := range am.Entries() {
				fn := value.KeyString(e.Key)
				cm, ok := e.Val.(*value.Map)
				if !ok {
					return nil, fmt.Errorf("%s: role %q rule %d: args for %q is not a mapping", path, role, i, fn)
				}
				var ar ArgRule
				if ar.RequireKwargs, err = requireKwargs(cm); err != nil {
					return nil, fmt.Errorf("%s: role %q rule %d: args for %q: %v", path, role, i, fn, err)
				}
				for _, f := range []struct {
					key string
					dst *[]string
				}{
					{"allow_sls", &ar.AllowSLS}, {"deny_sls", &ar.DenySLS},
					{"allow_mods", &ar.AllowMods}, {"deny_kwargs", &ar.DenyKwargs},
				} {
					if *f.dst, err = stringList(cm, f.key); err != nil {
						return nil, fmt.Errorf("%s: role %q rule %d: args for %q: %v", path, role, i, fn, err)
					}
				}
				r.Args[fn] = ar
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// requireKwargs reads `require_kwargs`: a mapping from a keyword name to the
// one value a call must give it.
//
// The value is a bool, a string or a number. A list, a mapping or null is
// refused at load: a requirement a call cannot be compared against exactly
// is one that would be quietly never met, or quietly always met, and a
// policy file is not where to find out which. The name is not a pattern,
// and an empty one is refused.
func requireKwargs(m *value.Map) (map[string]any, error) {
	v, ok := m.Get("require_kwargs")
	if !ok || v == nil {
		return nil, nil
	}
	rm, ok := v.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("`require_kwargs` is a mapping from a keyword to the value it must have, not %v", v)
	}
	out := make(map[string]any, rm.Len())
	for _, e := range rm.Entries() {
		name := value.KeyString(e.Key)
		if name == "" {
			return nil, fmt.Errorf("`require_kwargs` has a keyword with no name")
		}
		switch e.Val.(type) {
		case bool, string, int64, float64:
			out[name] = e.Val
		default:
			return nil, fmt.Errorf("`require_kwargs` %s is %v, which is not a bool, a string or a number", name, e.Val)
		}
	}
	return out, nil
}

// stringList reads a field that is a string or a list of strings.
//
// Anything else is an error. An item that was not a string used to be
// dropped, and a field that was neither left empty, with at most a parser
// warning: `deny_kwargs: [pillar, 5]` denied pillar alone, and
// `functions: [on]` -- a YAML 1.1 bool, not the string "on" -- granted
// nothing and said so nowhere. A deny list weaker than the one written is
// the failure a policy file must not have (issue 321, section 5).
func stringList(m *value.Map, key string) ([]string, error) {
	v, ok := m.Get(key)
	if !ok || v == nil {
		return nil, nil
	}
	switch t := v.(type) {
	case string:
		return []string{t}, nil
	case []any:
		out := make([]string, 0, len(t))
		for i, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("`%s` item %d is %v, which is not a string; quote it", key, i, item)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("`%s` is %v, which is neither a string nor a list of strings", key, v)
}

// RolesFor lists the roles bound to a principal, in a stable order.
func (p *Policy) RolesFor(principal string) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range p.Bindings {
		if !matchPrincipal(b.Principal, principal) {
			continue
		}
		for _, role := range b.Roles {
			if !seen[role] {
				seen[role] = true
				out = append(out, role)
			}
		}
	}
	sort.Strings(out)
	return out
}

// matchPrincipal compares a binding's principal to the authenticated
// one. A glob is permitted so that `node:lb*.prod` works, as SPEC 23.5
// writes it.
//
// The glob is Salt's, fnmatch, not Go's path.Match. path.Match's `*`
// stops at `/`, and a webhook's principal is `hook:<path>` with a path
// that may hold one, so `hook:*` gave nothing to `hook:github/push` and
// said nothing (issue 321; DIVERGENCE 5.465). Salt matches eauth names
// with fnmatch, and file_ignore_glob was moved off path.Match for the
// same reason (5.295); fnmatch.Match is that port. It widens as
// well as fixes: `hook:github/*` now also matches `hook:github/a/b`.
func matchPrincipal(pattern, principal string) bool {
	return pattern == principal || fnmatch.Match(pattern, principal)
}

// Authorize decides one request, resolving the principal's roles from
// the bindings. Deny by default: a request must match at least one rule
// entirely.
func (p *Policy) Authorize(req Request) Decision {
	if p == nil {
		return Decision{Reason: "this hub has no policy, so nothing is authorized"}
	}
	return p.AuthorizeAs(req, p.RolesFor(req.Principal))
}

// AuthorizeAs decides one request against a role set given rather than
// resolved.
//
// It is what a token needs: SPEC 23.6 freezes the roles at issue, so a
// role added to the principal afterwards must not widen a token already
// in someone's hands, and one taken away is a reason to revoke rather
// than a change that quietly applies mid-session.
func (p *Policy) AuthorizeAs(req Request, roles []string) Decision {
	if p == nil {
		return Decision{Reason: "there is no policy, so nothing is authorized"}
	}
	if len(roles) == 0 {
		return Decision{Reason: fmt.Sprintf("%s is bound to no role in %s", req.Principal, p.Path)}
	}

	// The most specific denial wins the message: an operator whose
	// target matched but whose function did not should be told that,
	// not "no rule matched".
	best := Decision{Reason: fmt.Sprintf("no rule in %v permits %s against %q", roles, req.Fun, req.Target)}
	var kind target.Kind
	if !req.Runner {
		k, ok := target.KindFromFlag(req.TargetKind)
		if !ok {
			// Dispatch refuses an unknown kind too; refusing it here
			// first means no rule is ever matched against a string
			// whose meaning nobody knows.
			return Decision{Reason: fmt.Sprintf("%q is not a target kind", req.TargetKind)}
		}
		kind = k
		if kind != target.Glob {
			best = Decision{Reason: fmt.Sprintf("no rule in %v permits %s against %s target %q",
				roles, req.Fun, kind, req.Target)}
		}
	}
	for _, role := range roles {
		for i, rule := range p.Roles[role] {
			if req.Runner {
				if !matchAny(rule.Runners, req.Fun) {
					continue
				}
				// A distinct high-privilege permission has to be
				// named, not matched. SPEC 22.1.
				if req.NeverWildcard && !namedExactly(rule.Runners, req.Fun) {
					best = Decision{Reason: fmt.Sprintf(
						"%s is never granted by a wildcard; name it in the role (SPEC 22.1)",
						req.Fun)}
					continue
				}
			} else {
				if !targetCovered(rule.Target, kind, req.Target) {
					continue
				}
				if !p.functionGranted(rule, req.Fun) || (req.NeverWildcard && !namedExactly(rule.Functions, req.Fun)) {
					if (p.ArbitraryCode[req.Fun] || req.NeverWildcard) && matchAny(rule.Functions, "*") {
						best = Decision{Reason: fmt.Sprintf(
							"%s runs arbitrary code and is never granted by a wildcard; name it in the role's functions (SPEC 23.5)",
							req.Fun)}
					}
					continue
				}
			}
			if reason := checkArgs(rule, req); reason != "" {
				best = Decision{Reason: reason}
				continue
			}
			return Decision{Allowed: true, Role: role, RuleIndex: i}
		}
	}
	return best
}

// functionGranted applies the wildcard rule of SPEC 23.5: `'*'` matches,
// but never a function that runs arbitrary code.
func (p *Policy) functionGranted(rule Rule, fun string) bool {
	for _, pattern := range rule.Functions {
		if pattern == fun {
			return true
		}
		if !matchGlob(pattern, fun) {
			continue
		}
		// Salt's `.*` grants everything, and everybody's Salt ACL
		// grants `.*`. A pattern here grants everything it matches
		// except the functions that are a shell by another name.
		if p.ArbitraryCode[fun] {
			continue
		}
		return true
	}
	return false
}

// VisibleNode reports whether a role set may see events about a node.
//
// SPEC 17.4 filters the event stream by the caller's policy so that a
// caller cannot subscribe to events about nodes it may not see. The
// test is target coverage and nothing narrower: a role that may run
// anything against `web*` may watch `web1.example`, whatever the
// function in the event happens to be. Making it depend on the function
// would mean an operator could act on a node and not see the result.
func (p *Policy) VisibleNode(roles []string, node string) bool {
	if p == nil {
		return false
	}
	for _, role := range roles {
		for _, rule := range p.Roles[role] {
			// A rule that grants only runners names no target and
			// says nothing about which nodes may be watched.
			if rule.Target == "" {
				continue
			}
			if literalCovered(rule.Target, node) {
				return true
			}
		}
	}
	return false
}

// HasAnyRule reports whether a role set grants anything at all.
//
// It is what an event with no node is filtered by: a job's, a key's, or
// the reactor's own events are not about a node, so target coverage has
// nothing to compare. Someone bound to no rule sees none of them.
func (p *Policy) HasAnyRule(roles []string) bool {
	if p == nil {
		return false
	}
	for _, role := range roles {
		if len(p.Roles[role]) > 0 {
			return true
		}
	}
	return false
}

// namedExactly reports whether a function appears literally in a
// rule's list, rather than being matched by a pattern in it.
func namedExactly(patterns []string, fun string) bool {
	for _, p := range patterns {
		if p == fun {
			return true
		}
	}
	return false
}

// targetCovered reports whether a rule's target pattern covers the
// target being asked for, read as the kind it will be dispatched under.
//
// A rule's target is a glob over node IDs, which is all SPEC 23.5 has
// ever written one as. So the question is always "is every node this
// request can reach inside that glob?", and how it is answered depends
// on the kind:
//
//   - glob: a comparison of *patterns*, not of nodes. `web*.prod`
//     covers `web1.prod` and covers `web*.prod`, and does not cover
//     `*`. A rule that granted `*` because the request said `*` would
//     be granting the whole estate to a role scoped to part of it.
//   - list: each element is one node ID, and each must be covered as a
//     literal. This is what `web1.prod,db1.prod` used to slip through:
//     as a string it has no `*?[`, so it was matched literally against
//     `web*.prod` -- whose `*` happily consumes `1.prod,db1` -- and
//     dispatch then split it and reached db1.prod.
//   - everything else (compound, nodegroup, pcre, grain, pillar,
//     ipcidr): only a rule of `*` covers it. Whether a compound
//     expression stays inside a glob is a question about the fleet at
//     the moment of dispatch, not about two strings, and a pcre that
//     looks like a node ID is still a pattern (`.` matches anything).
//     The hub could resolve the expression and check each matched
//     node, but the API authorizes before it forwards and has no fleet
//     to resolve against, and the two must make the same decision
//     (SPEC 5.2: both check). Refusing is the answer both can give.
//     A scoped operator who wants a set of nodes writes them as a list.
//
// Rule-text equality is kept only for glob. Before, a rule whose target
// was the literal text of a compound expression granted that expression
// by equality; but a rule's target is never compiled as a compound --
// VisibleNode reads it as a glob -- so that grant meant something the
// rest of the policy did not. DIVERGENCE 5.296.
func targetCovered(granted string, kind target.Kind, asked string) bool {
	if granted == "*" {
		return true
	}
	switch kind {
	case target.Glob:
		if granted == asked {
			return true
		}
		// A glob request is covered only by an identical or broader
		// glob, and "broader" is not decidable in general, so equality
		// is the rule. An operator who wants a wider grant writes a
		// wider rule.
		return literalCovered(granted, asked)
	case target.List:
		ids := target.ListIDs(asked)
		if len(ids) == 0 {
			// A list of nothing reaches nothing; there is nothing for a
			// scoped rule to say yes to, and saying yes to the empty
			// string is how an unconsidered case becomes a grant.
			return false
		}
		for _, id := range ids {
			if !literalCovered(granted, id) {
				return false
			}
		}
		return true
	}
	return false
}

// literalCovered reports whether one node ID falls inside a rule's glob.
//
// "A node ID" is held to pki.ValidateNodeID, the alphabet a node may
// enrol under: letters, digits and `.-_`. That is what makes it safe to
// match the string as a literal. Anything outside it -- a comma, a
// space, `\`, `*?[`, `|`, `@` -- is either a pattern or not an ID any
// node can have, and before this the test was only "contains no `*?[`",
// which let `web1.prod or db1.prod` and `web1.prod|db1.prod` through as
// "literals" under a `web*.prod` rule. Such a string is now covered only
// by rule-text equality or by `*`.
//
// The match itself is target.MatchGlob, the function dispatch matches
// node IDs with, so that "inside the rule's glob" and "matched by that
// glob as a target" cannot come to mean different things.
func literalCovered(granted, id string) bool {
	if granted == "*" {
		return true
	}
	if pki.ValidateNodeID(id) != nil {
		return false
	}
	return granted == id || target.MatchGlob(granted, id)
}

func matchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if p == s || matchGlob(p, s) {
			return true
		}
	}
	return false
}

// matchGlob matches a function, runner, keyword or SLS pattern, with
// Salt's fnmatch as matchPrincipal does. It was path.Match, whose `[!w]`
// is a set holding `!` and `w` where fnmatch's, Salt's and a shell's is
// "not w": `deny_sls: ['[!w]*']` denied `web` and let `secret` through,
// the reverse of what it says, and `pkg.[!r]*` granted `pkg.remove`.
// path.Match's `\` escape goes with it; fnmatch has none, as Salt has
// none. DIVERGENCE 5.472.
//
// Salt's own ACLs go further, through expr_match: a pattern that is not
// a glob match is tried again as a regular expression, which is how
// `.*` grants everything there. That is not done. A grammar in which
// every entry is two languages at once is what SPEC 23.5 exists to
// replace, and a regex here would widen every pattern in every policy.
func matchGlob(pattern, s string) bool {
	return pattern == "*" || fnmatch.Match(pattern, s)
}

// wireKwargs is the keywords the node will be sent: Kwarg, with `test` set
// from the dry-run flag the way job.WireKwargs sets it, over whatever Kwarg
// says. The map is a copy, because the request is the caller's.
func (r Request) wireKwargs() map[string]any {
	if !r.Test {
		return r.Kwarg
	}
	out := make(map[string]any, len(r.Kwarg)+1)
	for k, v := range r.Kwarg {
		out[k] = v
	}
	out["test"] = true
	return out
}

// checkRequired refuses a call that lacks a required keyword or gives it
// another value. Names are taken in sorted order, so the reason a role names
// is the same on every run.
func checkRequired(required, kwargs map[string]any, fun string) string {
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want := required[name]
		got, ok := kwargs[name]
		if !ok {
			return fmt.Sprintf("%s requires %s=%v from this role, and the call does not carry it", fun, name, want)
		}
		if !sameScalar(want, got) {
			return fmt.Sprintf("%s requires %s=%v from this role, and the call has %s=%v", fun, name, want, name, got)
		}
	}
	return ""
}

// sameScalar compares a required value with a call's. Types must agree: the
// string "true" is not the boolean true, which is the distinction a node
// refuses a `test` over (cmd/halite-node jobTestFlag). Numbers compare as
// numbers whichever of the integer and float types a YAML file and a JSON
// body gave them.
func sameScalar(want, got any) bool {
	switch w := want.(type) {
	case bool:
		g, ok := got.(bool)
		return ok && g == w
	case string:
		g, ok := got.(string)
		return ok && g == w
	}
	wf, ok1 := toFloat(want)
	gf, ok2 := toFloat(got)
	return ok1 && ok2 && wf == gf
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

// checkArgs applies the argument-level constraints, returning the
// reason a request is refused or the empty string.
func checkArgs(rule Rule, req Request) string {
	constraint, ok := rule.Args[req.Fun]
	if !ok {
		return ""
	}
	kwargs := req.wireKwargs()
	for _, kwarg := range constraint.DenyKwargs {
		for name := range kwargs {
			if name == kwarg || matchGlob(kwarg, name) {
				// Passing `pillar` on the command line is otherwise a
				// trivial way round pillar-based authorization.
				return fmt.Sprintf("%s may not be called with %q by this role", req.Fun, name)
			}
		}
	}
	if reason := checkRequired(constraint.RequireKwargs, kwargs, req.Fun); reason != "" {
		return reason
	}
	// The names deny_sls and allow_mods judge. For a job they are the
	// positional arguments, as they always were; for a runner they are
	// the names the orchestration runners will compile, which is a
	// different set -- see runnerNames.
	names := req.Arg
	if req.Runner && (len(constraint.DenySLS) > 0 || len(constraint.AllowMods) > 0) {
		var reason string
		names, reason = runnerNames(req)
		if reason != "" {
			return reason
		}
	}
	// deny_sls and allow_sls are judged on the one spelling the node's
	// compiler will load, state.CanonicalSLS, and an argument with any
	// other spelling is refused before either list is consulted. Matched
	// as written, `deny_sls: [secret.x]` refused `secret.x` and allowed
	// `secret/x`, `secret..x`, `.secret.x` and `secret/./x`, every one of
	// which a file server folds to secret/x.sls. A current node refuses
	// those spellings itself (DIVERGENCE 5.294), but that does not make
	// the hub's answer right: the hub logged the job as authorized, and
	// a node that predates 5.294 would have run it.
	//
	// Refusing rather than rewriting is the compiler's reason too: a
	// rewrite would make this check correct and leave several spellings
	// that work, and the next check written against req.Arg would have
	// the same hole. The node does not split `a,b` into two names today,
	// so neither does this; if it ever does, the split belongs here,
	// before canonicalising. DIVERGENCE 5.294.
	//
	// It reads `names`, not req.Arg: for a job they are the same, and for
	// a runner under deny_sls they are the orchestration names the runner
	// will compile, keyword and comma list included (DIVERGENCE 5.321).
	// Checking only the positional arguments would let a second spelling
	// through the keyword the same way the deny list was let through.
	if len(constraint.DenySLS) > 0 || len(constraint.AllowSLS) > 0 {
		for _, arg := range names {
			if _, err := state.CanonicalSLS(arg); err != nil {
				return fmt.Sprintf("%s refused for %s by this role: %v", arg, req.Fun, err)
			}
		}
	}
	if len(constraint.DenySLS) > 0 {
		for _, arg := range names {
			if matchAny(constraint.DenySLS, arg) {
				return fmt.Sprintf("%s is not permitted for %s by this role", arg, req.Fun)
			}
		}
	}
	if len(constraint.AllowSLS) > 0 {
		// The loop below judges the names a request gives, so a request
		// that gives none passed it — and a node handed a state job with
		// no names runs the highstate (runStateJob compiles the top file
		// unless there are positional arguments). `state.highstate` is
		// worse: the node ignores its arguments altogether, so naming an
		// allowed SLS bought the whole top file. Either way the run is
		// whatever the top file assigns, which is not a set of SLS names
		// this rule can check, so allow_sls refuses it. SPEC 23.5;
		// DIVERGENCE 5.320.
		//
		// `mods=` as a keyword does not rescue it: the node reads only
		// the positional arguments, so a keyword-only request is still
		// a highstate on the node.
		if req.Fun == "state.highstate" {
			return fmt.Sprintf("%s runs the whole highstate, which is not in the SLS names this role may apply (%s)",
				req.Fun, strings.Join(constraint.AllowSLS, ", "))
		}
		if len(req.Arg) == 0 {
			return fmt.Sprintf("%s with no SLS names runs the highstate, which is not in the SLS names this role may apply (%s)",
				req.Fun, strings.Join(constraint.AllowSLS, ", "))
		}
		for _, arg := range req.Arg {
			if !matchAny(constraint.AllowSLS, arg) {
				return fmt.Sprintf("%s is not in the SLS names this role may apply (%s)",
					arg, strings.Join(constraint.AllowSLS, ", "))
			}
		}
	}
	if len(constraint.AllowMods) > 0 {
		// A request that names nothing gave the loop nothing to refuse.
		// The runner refuses it too (`sls` is required), but a decision
		// that says "allowed" about a request whose names it never saw
		// is the defect this block exists to prevent, so it is refused
		// here rather than left to the signature. DIVERGENCE 5.321.
		if len(names) == 0 {
			return fmt.Sprintf("%s names no orchestration, so none of the modules this role may orchestrate (%s) can be checked",
				req.Fun, strings.Join(constraint.AllowMods, ", "))
		}
		for _, arg := range names {
			if !matchAny(constraint.AllowMods, arg) {
				return fmt.Sprintf("%s is not in the modules this role may orchestrate (%s)",
					arg, strings.Join(constraint.AllowMods, ", "))
			}
		}
	}
	return ""
}

// runnerNames is the set of orchestration names a runner request will
// compile, which is what deny_sls and allow_mods have to judge. It must
// agree with the hub, which binds the call against the runner's
// signature and then splits `sls` on commas (orchRequestFrom and
// tagGlobs in internal/hub). Judging anything narrower is a way round
// the constraint, and judging only the positional arguments was two:
//
//   - The orchestration runners take `sls` by keyword as readily as by
//     position, because sig.Bind binds either to the same parameter.
//     `halite-hub orch run` sends it *only* by keyword, so before this
//     a role with allow_mods was unconstrained on the operator's own
//     command line: every name it was asked for was allowed.
//   - `sls` is a comma list. `deploy.web,teardown.everything` is one
//     positional argument, `deploy.*` matches it (a glob `*` crosses
//     a comma), and the runner compiled both.
//
// So the names are every positional argument and the keyword `sls`,
// each split the way tagGlobs splits it. That is a superset of what
// the runner reads -- a second positional argument binds to `env`, not
// to `sls` -- and refusing a request because an argument that is not
// an orchestration name fails the pattern is the safe direction to be
// wrong in.
//
// A keyword `sls` that is not a string cannot be judged and is refused.
// The runner's signature refuses one too, but the policy should not
// have to rely on the signature to keep a value it never read from
// being allowed.
func runnerNames(req Request) ([]string, string) {
	var names []string
	for _, arg := range req.Arg {
		names = append(names, splitNames(arg)...)
	}
	if raw, ok := req.Kwarg["sls"]; ok {
		s, isString := raw.(string)
		if !isString {
			return nil, fmt.Sprintf("%s was given `sls` as %T, which this policy cannot check; give it as a string",
				req.Fun, raw)
		}
		names = append(names, splitNames(s)...)
	}
	return names, ""
}

// splitNames splits a comma list as tagGlobs in internal/hub does:
// on commas, trimmed, empties dropped.
func splitNames(spec string) []string {
	var out []string
	for _, part := range strings.Split(spec, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
