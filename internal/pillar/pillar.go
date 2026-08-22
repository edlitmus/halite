// Package pillar compiles a node's pillar.
//
// Pillar is the secret-bearing path, so two properties matter more here
// than anywhere else. An external source that fails is a hard error rather
// than a partial pillar, because a partial pillar silently applies a state
// with a missing value. And the grains a node may target on are an
// allowlist, because a node controls its own grains and could otherwise
// claim `role: database` and be handed the database pillar. SPEC sections
// 12.4 and 12.7.
package pillar

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// TrustedGrains is the default allowlist of SPEC section 12.4: the grains
// a node may target pillar on. Custom grains are excluded, and adding one
// is a recorded decision.
var TrustedGrains = []string{
	"id", "os", "os_family", "osrelease", "kernel", "cpuarch", "virtual", "fips_mode",
}

// Config controls a compilation.
type Config struct {
	// Env is the pillar environment. Empty means "base".
	Env    string
	NodeID string
	JobID  string

	// Grains are the node's grains. Only the trusted subset reaches
	// targeting; the whole set reaches templates, because a template that
	// reads a grain is not a trust decision.
	Grains *value.Map
	// NodeAttrs are the hub-authoritative attributes, exposed to
	// targeting under `node:` rather than `grain:`. A node cannot forge
	// them. SPEC section 12.4.
	NodeAttrs *value.Map
	// ConfigValues is the effective configuration, redacted.
	ConfigValues *value.Map

	// TrustedGrains overrides the default allowlist.
	TrustedGrains []string
	// Strategy is the pillar source merging strategy.
	Strategy value.Strategy
	// MergeLists concatenates lists under recurse and smart.
	MergeLists bool

	Salt template.Dispatcher
	// NewSalt builds the dispatcher for one render, given the pillar
	// compiled so far. It takes precedence over Salt.
	NewSalt    func(partial *value.Map) template.Dispatcher
	Undefined  template.UndefinedMode
	Nodegroups target.Nodegroups
	YAMLBool11 *bool
	// Nondeterministic restores Salt's unseeded template randomness.
	Nondeterministic bool
	// TemplateOptions carries the renderer settings a tree sets,
	// such as trim_blocks. Nil takes the defaults.
	TemplateOptions *template.Options
	// GPG configures the gpg renderer of SPEC section 12.6.
	GPG render.GPGOptions
	// OnSecret receives every decrypted value, for the redactor of
	// SPEC 26.1.
	OnSecret func(string)

	// Local marks a development compilation from a local pillar root.
	// `halite-node call pillar.items --local` sets it; it never runs
	// against the hub's roots. SPEC section 12.1.
	Local bool
}

// AuditEntry records why one pillar SLS was delivered to this node.
//
// Every compilation records which top entries matched and on what basis,
// so that an unexpected secret delivery is reconstructable. SPEC section
// 12.4.
type AuditEntry struct {
	Env    string
	Target string
	// Basis names how the target matched: glob, grain, compound, and so
	// on.
	Basis string
	SLS   []string
}

func (a AuditEntry) String() string {
	return fmt.Sprintf("%s: %q (%s) delivered %s", a.Env, a.Target, a.Basis, strings.Join(a.SLS, ", "))
}

// Compiled is a node's pillar and the record of how it was assembled.
type Compiled struct {
	Pillar *value.Map
	Audit  []AuditEntry
	// SLS lists the pillar files that contributed, in merge order.
	SLS      []string
	Diags    state.Diags
	Warnings []render.Warning
}

// Err reports the compilation errors as one error, or nil.
func (c *Compiled) Err() error { return c.Diags.Err() }

// Compiler assembles a pillar.
type Compiler struct {
	Loader state.Loader
	Config Config
}

func (c *Compiler) env() string {
	if c.Config.Env != "" {
		return c.Config.Env
	}
	return "base"
}

func (c *Compiler) trusted() []string {
	if len(c.Config.TrustedGrains) > 0 {
		return c.Config.TrustedGrains
	}
	return TrustedGrains
}

// Compile assembles this node's pillar.
func (c *Compiler) Compile() *Compiled {
	out := &Compiled{Pillar: value.NewMap(32)}

	matched, optional := c.resolveTop(out)
	env := c.env()
	seen := map[string]bool{}
	for _, name := range matched {
		c.mergeSLS(out, env, name, seen, nil, optional[name])
	}
	return out
}

// targetNode is what a pillar top expression is evaluated against.
//
// The grains it sees are the trusted subset only. A node that sets a
// custom grain cannot use it to select pillar unless an operator has
// added it to the allowlist, which is a recorded decision rather than an
// accident.
func (c *Compiler) targetNode() target.Node {
	trusted := value.NewMap(len(c.trusted()))
	for _, name := range c.trusted() {
		if v, ok := c.Config.Grains.Get(name); ok {
			trusted.Set(name, v)
		}
	}
	// The hub-authoritative attributes are exposed under `node:` so that
	// a top file can target on something a node cannot forge.
	if c.Config.NodeAttrs != nil && c.Config.NodeAttrs.Len() > 0 {
		trusted.Set("node", c.Config.NodeAttrs)
	}
	return target.Node{ID: c.Config.NodeID, Grains: trusted}
}

// resolveTop reads the pillar top file and returns the matched SLS names.
// resolveTop returns the SLS names this node matches, and the subset of
// them declared under an `ignore_missing: true` target. Salt honours that
// directive in a pillar top file and not in a state top file, so this is
// the only place it applies.
func (c *Compiler) resolveTop(out *Compiled) ([]string, map[string]bool) {
	env := c.env()
	src, path, err := c.Loader.Source(env, state.TopName)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			// A pillar tree with no top file delivers nothing, which is a
			// legitimate configuration rather than an error.
			return nil, nil
		}
		out.Diags.Add(value.Pos{File: state.TopName}, state.TopName, "",
			"reading the pillar top file for %q: %v", env, err)
		return nil, nil
	}

	res, err := render.Render(src, c.renderOptions(env, state.TopName, path, nil))
	out.Warnings = append(out.Warnings, res.Warnings...)
	if err != nil {
		out.Diags.Add(value.Pos{File: path}, state.TopName, "", "%v", err)
		return nil, nil
	}

	optional := map[string]bool{}
	top, ok := res.Value.(*value.Map)
	if !ok {
		if res.Value == nil {
			return nil, nil
		}
		out.Diags.Add(value.Pos{File: path}, state.TopName, "",
			"a pillar top file must hold a mapping of environments, found %s", value.TypeName(res.Value))
		return nil, nil
	}

	node := c.targetNode()
	var matched []string
	for _, envEntry := range top.Entries() {
		topEnv := value.KeyString(envEntry.Key)
		targets, ok := envEntry.Val.(*value.Map)
		if !ok {
			out.Diags.Add(envEntry.ValPos, state.TopName, "",
				"the %q environment in the pillar top file must hold a mapping of target expressions", topEnv)
			continue
		}
		for _, te := range targets.Entries() {
			expr := value.KeyString(te.Key)
			basis, err := c.checkTargetIsPermitted(expr, state.TopMatchKind(te.Val))
			if err != nil {
				out.Diags.Add(te.KeyPos, state.TopName, "", "%v", err)
				continue
			}
			matcher, err := state.CompileTopTarget(expr, te.Val, c.Config.Nodegroups)
			if err != nil {
				out.Diags.Add(te.KeyPos, state.TopName, "", "%v", err)
				continue
			}
			if !matcher.Match(node) {
				continue
			}
			names, ignoreMissing := topSLSNames(te.Val, te.ValPos, &out.Diags)
			matched = appendUnique(matched, names)
			if ignoreMissing {
				for _, n := range names {
					optional[n] = true
				}
			}
			out.Audit = append(out.Audit, AuditEntry{Env: topEnv, Target: expr, Basis: basis, SLS: names})
		}
	}
	return matched, optional
}

// checkTargetIsPermitted refuses a pillar top expression that targets on
// pillar, and reports which grains an expression relies on so that an
// untrusted one can be refused by name.
func (c *Compiler) checkTargetIsPermitted(expr, matchKind string) (string, error) {
	// Pillar cannot target on pillar: it does not exist yet, and
	// pretending it does produces an ordering-dependent result. SPEC
	// section 12.4.
	for _, sigil := range []string{"I@", "J@"} {
		if strings.Contains(expr, sigil) {
			return "", fmt.Errorf(
				"pillar top expression %q targets on pillar, which is not available while pillar is being compiled", expr)
		}
	}

	if matchKind == "pillar" || matchKind == "pillar_pcre" {
		return "", fmt.Errorf(
			"pillar top expression %q targets on pillar, which is not available while pillar is being compiled", expr)
	}

	grains := grainNamesIn(expr)
	// `- match: grain` names the grain in the expression rather than
	// with a G@ sigil, and is the spelling an existing Salt tree uses.
	// Missing it meant the rule of SPEC 12.4 was neither enforced nor
	// reported: the target compiled, matched nothing, because the node a
	// pillar target sees carries only the trusted grains, and the file
	// was silently absent from the pillar.
	if matchKind == "grain" || matchKind == "grain_pcre" {
		name, _, _ := strings.Cut(expr, ":")
		if name != "" {
			grains = append(grains, name)
		}
	}
	if len(grains) == 0 {
		return "glob", nil
	}
	trusted := map[string]bool{}
	for _, g := range c.trusted() {
		trusted[g] = true
	}
	var untrusted []string
	for _, g := range grains {
		// A `node:` path is hub-authoritative and always permitted.
		if strings.HasPrefix(g, "node:") || g == "node" {
			continue
		}
		if !trusted[g] {
			untrusted = append(untrusted, g)
		}
	}
	if len(untrusted) > 0 {
		sort.Strings(untrusted)
		return "", fmt.Errorf(
			"pillar top expression %q targets on the grain(s) %s, which a node controls and which are not in pillar_trusted_grains; "+
				"add them deliberately, or move the attribute to a hub-authoritative node attribute (SPEC section 12.4)",
			expr, strings.Join(untrusted, ", "))
	}
	return "grain", nil
}

// grainNamesIn extracts the grain names a target expression relies on.
func grainNamesIn(expr string) []string {
	var out []string
	for _, sigil := range []string{"G@", "P@"} {
		off := 0
		for {
			i := strings.Index(expr[off:], sigil)
			if i < 0 {
				break
			}
			rest := expr[off+i+len(sigil):]
			end := strings.IndexAny(rest, ": ")
			if end < 0 {
				end = len(rest)
			}
			if end > 0 {
				out = append(out, rest[:end])
			}
			off += i + len(sigil)
		}
	}
	return out
}

func topSLSNames(v any, pos value.Pos, diags *state.Diags) (names []string, ignoreMissing bool) {
	items, ok := v.([]any)
	if !ok {
		diags.Add(pos, state.TopName, "", "a pillar top target must hold a list of SLS names, found %s", value.TypeName(v))
		return nil, false
	}
	for _, item := range items {
		switch t := item.(type) {
		case string:
			names = append(names, t)
		case *value.Map:
			if t.Len() != 1 {
				continue
			}
			e := t.Entries()[0]
			switch value.KeyString(e.Key) {
			case "match":
				// A directive, not an SLS name.
			case "ignore_missing":
				ignoreMissing = value.Truthy(e.Val)
			}
		}
	}
	return names, ignoreMissing
}

// mergeSLS renders one pillar SLS, follows its includes, and merges the
// result.
func (c *Compiler) mergeSLS(out *Compiled, env, name string, seen map[string]bool, stack []string, optional bool) {
	key := env + "|" + name
	for i, s := range stack {
		if s == key {
			out.Diags.Warn(value.Pos{File: name}, name, "",
				"pillar include cycle: %s; the second arrival is skipped",
				strings.Join(append(append([]string{}, stack[i:]...), key), " -> "))
			return
		}
	}
	if seen[key] {
		return
	}
	seen[key] = true

	src, path, err := c.Loader.Source(env, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			if optional {
				// `ignore_missing: true` in the top file. A tree that
				// names a pillar file per host and ships only some of
				// them is the reason Salt has this.
				return
			}
			out.Diags.Add(value.Pos{File: name}, name, "",
				"pillar sls %q was not found in environment %q", name, env)
			return
		}
		out.Diags.Add(value.Pos{File: name}, name, "", "reading pillar sls %q: %v", name, err)
		return
	}

	// The pillar being built so far is passed to the renderer, so that
	// `salt['pillar.get']` inside pillar rendering resolves against the
	// partially built tree in declaration order. SPEC section 10.2.7.
	res, err := render.Render(src, c.renderOptions(env, name, path, out.Pillar))
	out.Warnings = append(out.Warnings, res.Warnings...)
	if err != nil {
		out.Diags.Add(value.Pos{File: path}, name, "", "%v", err)
		return
	}
	if res.Value == nil {
		out.SLS = append(out.SLS, name)
		return
	}
	body, ok := res.Value.(*value.Map)
	if !ok {
		out.Diags.Add(value.Pos{File: path}, name, "",
			"a pillar sls must hold a mapping, found %s", value.TypeName(res.Value))
		return
	}

	// Includes are merged before the including file, so the including
	// file's own keys win.
	if inc, ok := body.Get("include"); ok {
		for _, n := range includeNames(inc, name) {
			// An include names a file the author knew about, so a
			// missing one is an error even under ignore_missing.
			c.mergeSLS(out, env, n, seen, append(stack, key), false)
		}
		body = withoutKey(body, "include")
	}

	out.Pillar = value.Merge(out.Pillar, body, value.MergeOpts{
		Strategy:   c.Config.Strategy,
		MergeLists: c.Config.MergeLists,
	}).(*value.Map)
	out.SLS = append(out.SLS, name)
}

func includeNames(v any, from string) []string {
	items, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok {
			return []string{resolveRelative(s, from)}
		}
		return nil
	}
	var out []string
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, resolveRelative(s, from))
		}
	}
	return out
}

// resolveRelative expands the leading-dot relative include, where `.foo`
// inside `web.nginx` means `web.foo`.
func resolveRelative(name, sls string) string {
	if !strings.HasPrefix(name, ".") {
		return name
	}
	parent := ""
	if i := strings.LastIndex(sls, "."); i >= 0 {
		parent = sls[:i]
	}
	rest := strings.TrimLeft(name, ".")
	up := len(name) - len(rest) - 1
	for i := 0; i < up; i++ {
		if j := strings.LastIndex(parent, "."); j >= 0 {
			parent = parent[:j]
			continue
		}
		parent = ""
	}
	if parent == "" {
		return rest
	}
	return parent + "." + rest
}

func withoutKey(m *value.Map, key string) *value.Map {
	out := value.NewMap(m.Len())
	for _, e := range m.Entries() {
		if value.KeyString(e.Key) == key {
			continue
		}
		out.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
	}
	return out
}

// saltFor builds the `salt` dispatcher for one pillar render. A pillar
// file calling salt['pillar.get'] must see the pillar built so far and
// not the finished one, which does not exist yet, so the dispatcher is
// built per render rather than held on the configuration.
func (c *Compiler) saltFor(partial *value.Map) template.Dispatcher {
	if c.Config.NewSalt != nil {
		return c.Config.NewSalt(partial)
	}
	return c.Config.Salt
}

func (c *Compiler) renderOptions(env, sls, path string, partial *value.Map) render.Options {
	return render.Options{
		File:             path,
		SLS:              sls,
		Env:              env,
		PillarEnv:        env,
		NodeID:           c.Config.NodeID,
		JobID:            c.Config.JobID,
		Grains:           c.Config.Grains,
		Pillar:           partial,
		Config:           c.Config.ConfigValues,
		Salt:             c.saltFor(partial),
		Loader:           c.Loader.Templates(env),
		Undefined:        c.Config.Undefined,
		YAMLBool11:       c.Config.YAMLBool11,
		Nondeterministic: c.Config.Nondeterministic,
		TemplateOptions:  c.Config.TemplateOptions,
		GPG:              c.Config.GPG,
		OnSecret:         c.Config.OnSecret,
	}
}

func appendUnique(dst, src []string) []string {
	for _, s := range src {
		found := false
		for _, existing := range dst {
			if existing == s {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, s)
		}
	}
	return dst
}
