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
	"fmt"
	"path"
	"sort"
	"strings"

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
	Fun       string
	Arg       []string
	Kwarg     map[string]any
	// Runner marks a runner call rather than a fleet job, which is
	// granted by `runners:` rather than by `functions:`.
	Runner bool
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
			b.Roles = stringList(m, "roles")
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
		r.Functions = stringList(m, "functions")
		r.Runners = stringList(m, "runners")
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
				r.Args[fn] = ArgRule{
					AllowSLS:   stringList(cm, "allow_sls"),
					DenySLS:    stringList(cm, "deny_sls"),
					AllowMods:  stringList(cm, "allow_mods"),
					DenyKwargs: stringList(cm, "deny_kwargs"),
				}
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func stringList(m *value.Map, key string) []string {
	v, ok := m.Get(key)
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
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
func matchPrincipal(pattern, principal string) bool {
	if pattern == principal {
		return true
	}
	ok, err := path.Match(pattern, principal)
	return err == nil && ok
}

// Authorize decides one request. Deny by default: a request must match
// at least one rule entirely.
func (p *Policy) Authorize(req Request) Decision {
	if p == nil {
		return Decision{Reason: "this hub has no policy, so nothing is authorized"}
	}
	roles := p.RolesFor(req.Principal)
	if len(roles) == 0 {
		return Decision{Reason: fmt.Sprintf("%s is bound to no role in %s", req.Principal, p.Path)}
	}

	// The most specific denial wins the message: an operator whose
	// target matched but whose function did not should be told that,
	// not "no rule matched".
	best := Decision{Reason: fmt.Sprintf("no rule in %v permits %s against %q", roles, req.Fun, req.Target)}
	for _, role := range roles {
		for i, rule := range p.Roles[role] {
			if req.Runner {
				if !matchAny(rule.Runners, req.Fun) {
					continue
				}
			} else {
				if !targetCovered(rule.Target, req.Target) {
					continue
				}
				if !p.functionGranted(rule, req.Fun) {
					if p.ArbitraryCode[req.Fun] && matchAny(rule.Functions, "*") {
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

// targetCovered reports whether a rule's target pattern covers the
// target being asked for.
//
// This is a comparison of *patterns*, not of nodes: `web*.prod` covers
// `web1.prod` and covers `web*.prod`, and does not cover `*`. A rule
// that granted `*` because the request said `*` would be granting the
// whole estate to a role scoped to part of it.
func targetCovered(granted, asked string) bool {
	if granted == "*" {
		return true
	}
	if granted == asked {
		return true
	}
	// A literal request inside a granted glob.
	if !strings.ContainsAny(asked, "*?[") {
		return matchGlob(granted, asked)
	}
	// A glob request is covered only by an identical or broader glob,
	// and "broader" is not decidable in general, so equality is the
	// rule. An operator who wants a wider grant writes a wider rule.
	return false
}

func matchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if p == s || matchGlob(p, s) {
			return true
		}
	}
	return false
}

func matchGlob(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

// checkArgs applies the argument-level constraints, returning the
// reason a request is refused or the empty string.
func checkArgs(rule Rule, req Request) string {
	constraint, ok := rule.Args[req.Fun]
	if !ok {
		return ""
	}
	for _, kwarg := range constraint.DenyKwargs {
		for name := range req.Kwarg {
			if name == kwarg || matchGlob(kwarg, name) {
				// Passing `pillar` on the command line is otherwise a
				// trivial way round pillar-based authorization.
				return fmt.Sprintf("%s may not be called with %q by this role", req.Fun, name)
			}
		}
	}
	if len(constraint.DenySLS) > 0 {
		for _, arg := range req.Arg {
			if matchAny(constraint.DenySLS, arg) {
				return fmt.Sprintf("%s is not permitted for %s by this role", arg, req.Fun)
			}
		}
	}
	if len(constraint.AllowSLS) > 0 {
		for _, arg := range req.Arg {
			if !matchAny(constraint.AllowSLS, arg) {
				return fmt.Sprintf("%s is not in the SLS names this role may apply (%s)",
					arg, strings.Join(constraint.AllowSLS, ", "))
			}
		}
	}
	if len(constraint.AllowMods) > 0 {
		for _, arg := range req.Arg {
			if !matchAny(constraint.AllowMods, arg) {
				return fmt.Sprintf("%s is not in the modules this role may orchestrate (%s)",
					arg, strings.Join(constraint.AllowMods, ", "))
			}
		}
	}
	return ""
}
