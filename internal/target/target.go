// Package target evaluates a target expression against a node.
//
// Targeting is evaluated on the hub, against the last-known grains,
// pillar, and connection state of each node. Salt evaluates most target
// types on the managed endpoint instead, which means every endpoint sees
// every job, and a compromised one learns the target expression and
// arguments of jobs meant for others. Here a node receives only the jobs
// it matched. SPEC section 8.
//
// The same matchers evaluate a top file, which is why this package is
// shared by the state compiler and the pillar compiler.
package target

import (
	"fmt"
	"net/netip"
	"path"
	"strings"

	"github.com/edlitmus/halite/internal/regexcompat"
	"github.com/edlitmus/halite/internal/value"
)

// Kind names a target type. The flags are Salt's, so operator muscle
// memory transfers.
type Kind int

const (
	// Glob matches the node ID with path.Match semantics. The default.
	Glob Kind = iota
	// List is an exact set of node IDs, flag -L.
	List
	// Regex matches the node ID, flag -E. RE2 only.
	Regex
	// Grain matches a colon-delimited grain path, flag -G.
	Grain
	// GrainRegex matches a grain with a pattern, flag -P.
	GrainRegex
	// Pillar matches compiled pillar, flag -I.
	Pillar
	// PillarRegex matches compiled pillar with a pattern, flag -J.
	PillarRegex
	// Subnet matches an address or a CIDR against the ipv4 and ipv6
	// grains, flag -S.
	Subnet
	// Nodegroup names a compound expression from configuration, flag -N.
	Nodegroup
	// Compound is the full expression grammar, flag -C.
	Compound
)

// kindLetters map the compound-expression type sigils to kinds.
var kindLetters = map[string]Kind{
	"G": Grain,
	"P": GrainRegex,
	"I": Pillar,
	"J": PillarRegex,
	"L": List,
	"S": Subnet,
	"E": Regex,
	"N": Nodegroup,
}

// KindFromFlag resolves a CLI flag letter to a kind.
func KindFromFlag(flag string) (Kind, bool) {
	switch flag {
	case "", "glob":
		return Glob, true
	case "L", "list":
		return List, true
	case "E", "pcre":
		return Regex, true
	case "G", "grain":
		return Grain, true
	case "P", "grain_pcre":
		return GrainRegex, true
	case "I", "pillar":
		return Pillar, true
	case "J", "pillar_pcre":
		return PillarRegex, true
	case "S", "ipcidr":
		return Subnet, true
	case "N", "nodegroup":
		return Nodegroup, true
	case "C", "compound":
		return Compound, true
	}
	return Glob, false
}

// Node is everything a matcher may read about one node.
type Node struct {
	ID     string
	Grains *value.Map
	Pillar *value.Map
	// GrainsStale marks a node whose cached grains are older than
	// grain_stale_after, so a caller can annotate the result rather than
	// hide the hazard. SPEC section 8.3.
	GrainsStale bool
}

// Nodegroups resolves a nodegroup name to its expression. A nodegroup may
// reference another with N@name, to a depth of 10, and a cycle is a
// configuration error detected at load rather than at use.
type Nodegroups map[string]string

// Error is an unparseable or unusable expression. It names the offending
// token and its column, because a target expression that degrades into a
// broader match is how a job reaches a host it was never meant for.
type Error struct {
	Expr string
	Col  int
	Msg  string
}

func (e *Error) Error() string {
	if e.Col > 0 {
		return fmt.Sprintf("target expression %q: column %d: %s", e.Expr, e.Col, e.Msg)
	}
	return fmt.Sprintf("target expression %q: %s", e.Expr, e.Msg)
}

// Matcher is a compiled target expression.
type Matcher struct {
	kind  Kind
	expr  string
	ast   node
	group Nodegroups
}

// Compile prepares a target expression of the given kind. Compilation is
// separate from matching so that a bad expression is an error before any
// node is considered, and so that a top file with many entries compiles
// each one once.
func Compile(kind Kind, expr string, groups Nodegroups) (*Matcher, error) {
	m := &Matcher{kind: kind, expr: expr, group: groups}
	switch kind {
	case Compound:
		p := &parser{src: expr, groups: groups}
		ast, err := p.parse()
		if err != nil {
			return nil, err
		}
		m.ast = ast
		return m, nil

	case Nodegroup:
		resolved, err := resolveNodegroup(expr, groups, 0, nil)
		if err != nil {
			return nil, err
		}
		p := &parser{src: resolved, groups: groups}
		ast, err := p.parse()
		if err != nil {
			return nil, err
		}
		m.ast = ast
		return m, nil

	default:
		leaf, err := compileLeaf(kind, expr)
		if err != nil {
			return nil, err
		}
		m.ast = leaf
		return m, nil
	}
}

// CompileAuto guesses the kind from the expression, which is how a top
// file entry is written: a bare glob unless it carries a compound sigil.
func CompileAuto(expr string, groups Nodegroups) (*Matcher, error) {
	if looksCompound(expr) {
		return Compile(Compound, expr, groups)
	}
	return Compile(Glob, expr, groups)
}

// looksCompound reports whether an expression uses the compound grammar.
func looksCompound(expr string) bool {
	if strings.ContainsAny(expr, "()") {
		return true
	}
	for word := range strings.FieldsSeq(expr) {
		switch word {
		case "and", "or", "not":
			return true
		}
		if i := strings.Index(word, "@"); i > 0 {
			if _, ok := kindLetters[word[:i]]; ok {
				return true
			}
		}
	}
	return false
}

// Match evaluates the expression against a node.
func (m *Matcher) Match(n Node) bool {
	if m.ast == nil {
		return false
	}
	return m.ast.match(n)
}

// Expr returns the source expression, for a diagnostic.
func (m *Matcher) Expr() string { return m.expr }

// resolveNodegroup expands a nodegroup name into its expression, following
// N@ references and refusing a cycle.
func resolveNodegroup(name string, groups Nodegroups, depth int, seen []string) (string, error) {
	const maxDepth = 10
	if depth > maxDepth {
		return "", &Error{Expr: name, Msg: fmt.Sprintf("nodegroup references nest deeper than %d levels", maxDepth)}
	}
	for _, s := range seen {
		if s == name {
			return "", &Error{
				Expr: name,
				Msg:  fmt.Sprintf("nodegroup cycle: %s -> %s", strings.Join(seen, " -> "), name),
			}
		}
	}
	expr, ok := groups[name]
	if !ok {
		return "", &Error{Expr: name, Msg: fmt.Sprintf("nodegroup %q is not defined", name)}
	}

	seen = append(seen, name)
	var out strings.Builder
	for i := 0; i < len(expr); i++ {
		if !strings.HasPrefix(expr[i:], "N@") {
			out.WriteByte(expr[i])
			continue
		}
		j := i + 2
		for j < len(expr) && expr[j] != ' ' && expr[j] != ')' && expr[j] != '(' {
			j++
		}
		inner, err := resolveNodegroup(expr[i+2:j], groups, depth+1, seen)
		if err != nil {
			return "", err
		}
		out.WriteString("( " + inner + " )")
		i = j - 1
	}
	return out.String(), nil
}

// ValidateNodegroups checks every nodegroup at load rather than at use, so
// that a cycle or a missing reference is a configuration error the
// operator sees at start.
func ValidateNodegroups(groups Nodegroups) error {
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		expanded, err := resolveNodegroup(name, groups, 0, nil)
		if err != nil {
			return err
		}
		p := &parser{src: expanded, groups: groups}
		if _, err := p.parse(); err != nil {
			return fmt.Errorf("nodegroup %q: %w", name, err)
		}
	}
	return nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ---- leaf matchers ----

type node interface{ match(Node) bool }

type funcNode func(Node) bool

func (f funcNode) match(n Node) bool { return f(n) }

func compileLeaf(kind Kind, expr string) (node, error) {
	switch kind {
	case Glob:
		return funcNode(func(n Node) bool { return globMatch(expr, n.ID) }), nil

	case List:
		ids := map[string]bool{}
		for _, id := range strings.Split(expr, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids[id] = true
			}
		}
		return funcNode(func(n Node) bool { return ids[n.ID] }), nil

	case Regex:
		re, err := regexcompat.Compile(expr)
		if err != nil {
			return nil, &Error{Expr: expr, Msg: err.Error()}
		}
		return funcNode(func(n Node) bool { return re.MatchString(n.ID) }), nil

	case Grain, Pillar:
		key, pattern, err := splitKeyValue(expr)
		if err != nil {
			return nil, err
		}
		pick := pickGrains
		if kind == Pillar {
			pick = pickPillar
		}
		return funcNode(func(n Node) bool {
			v, ok := value.Traverse(pick(n), key, ":")
			if !ok {
				return false
			}
			return matchScalarOrList(v, func(s string) bool { return globMatch(pattern, s) })
		}), nil

	case GrainRegex, PillarRegex:
		key, pattern, err := splitKeyValue(expr)
		if err != nil {
			return nil, err
		}
		re, err := regexcompat.Compile(pattern)
		if err != nil {
			return nil, &Error{Expr: expr, Msg: err.Error()}
		}
		pick := pickGrains
		if kind == PillarRegex {
			pick = pickPillar
		}
		return funcNode(func(n Node) bool {
			v, ok := value.Traverse(pick(n), key, ":")
			if !ok {
				return false
			}
			return matchScalarOrList(v, re.MatchString)
		}), nil

	case Subnet:
		return compileSubnet(expr)
	}
	return nil, &Error{Expr: expr, Msg: "unsupported target type"}
}

func pickGrains(n Node) any {
	if n.Grains == nil {
		return value.NewMap(0)
	}
	return n.Grains
}

func pickPillar(n Node) any {
	if n.Pillar == nil {
		return value.NewMap(0)
	}
	return n.Pillar
}

// splitKeyValue divides `os_family:Debian` into its parts. A grain target
// without a value matches the grain's presence.
func splitKeyValue(expr string) (key, pattern string, err error) {
	i := strings.LastIndex(expr, ":")
	if i < 0 {
		return expr, "*", nil
	}
	key, pattern = expr[:i], expr[i+1:]
	if key == "" {
		return "", "", &Error{Expr: expr, Msg: "the grain or pillar name is empty"}
	}
	return key, pattern, nil
}

// matchScalarOrList applies a predicate to a value, or to any member when
// the value is a list. Salt's ipv4 and roles grains are lists, and a target
// that only matched scalars would silently miss them.
func matchScalarOrList(v any, pred func(string) bool) bool {
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			if pred(value.KeyString(item)) {
				return true
			}
		}
		return false
	case *value.Map:
		return false
	default:
		return pred(value.KeyString(v))
	}
}

// MatchGlob reports whether a node ID matches a glob, by the same rule
// the fleet is targeted with.
//
// Exported so that a bootstrap token's node scope and a job's target
// cannot come to disagree about what `web*.example` means.
func MatchGlob(pattern, nodeID string) bool { return globMatch(pattern, nodeID) }

// globMatch is path.Match with a whole-string requirement, which is what
// Salt's glob targeting does.
func globMatch(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	ok, err := path.Match(pattern, s)
	if err != nil {
		// A malformed pattern must not become a wildcard.
		return false
	}
	return ok
}

// compileSubnet matches an address or a CIDR against the node's address
// grains.
func compileSubnet(expr string) (node, error) {
	if prefix, err := netip.ParsePrefix(expr); err == nil {
		p := prefix.Masked()
		return funcNode(func(n Node) bool {
			return anyAddress(n, func(a netip.Addr) bool { return p.Contains(a) })
		}), nil
	}
	addr, err := netip.ParseAddr(expr)
	if err != nil {
		return nil, &Error{Expr: expr, Msg: fmt.Sprintf("%q is neither an address nor a network", expr)}
	}
	return funcNode(func(n Node) bool {
		return anyAddress(n, func(a netip.Addr) bool { return a == addr })
	}), nil
}

func anyAddress(n Node, pred func(netip.Addr) bool) bool {
	if n.Grains == nil {
		return false
	}
	for _, key := range []string{"ipv4", "ipv6"} {
		v, ok := n.Grains.Get(key)
		if !ok {
			continue
		}
		list, ok := v.([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				continue
			}
			a, err := netip.ParseAddr(s)
			if err != nil {
				continue
			}
			if pred(a.Unmap()) {
				return true
			}
		}
	}
	return false
}
