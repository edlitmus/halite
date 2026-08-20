package state

import (
	"errors"
	"sort"

	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/value"
)

// TopName is the file every environment's top file lives in.
const TopName = "top"

// TopEntry is one matched target in a top file.
type TopEntry struct {
	Env    string
	Target string
	SLS    []string
	Pos    value.Pos
	// From is the environment whose top file declared it, which differs
	// from Env when a top file cross-declares.
	From string
}

// Top is a resolved top file: every environment's entries, whether or not
// they matched.
type Top struct {
	Entries []TopEntry
	// Matched holds the SLS names this node matched, per environment, in
	// declaration order.
	Matched map[string][]string
}

// resolveTop reads the top file from every configured environment, renders
// it, matches this node, and produces the ordered SLS list per
// environment. SPEC section 11.2 step 1.
func (c *Compiler) resolveTop() (map[string][]string, Diags) {
	var diags Diags
	strategy := c.Config.TopMergeStrategy
	if strategy == "" {
		strategy = "merge"
	}

	envs := c.Loader.Envs()
	if len(envs) == 0 {
		envs = []string{c.env()}
	}

	// perEnv[declaringEnv] holds what that environment's top file said.
	perEnv := map[string]map[string][]string{}
	var found []string

	for _, env := range envs {
		src, filePath, err := c.Loader.Source(env, TopName)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			diags.Add(value.Pos{File: TopName}, TopName, "", "reading the top file for %q: %v", env, err)
			continue
		}
		found = append(found, env)

		res, err := render.Render(src, c.renderOptions(env, TopName, filePath))
		if err != nil {
			diags.Add(value.Pos{File: filePath}, TopName, "", "%v", err)
			continue
		}
		perEnv[env] = c.matchTop(res.Value, filePath, &diags)
	}

	if len(found) == 0 {
		diags.Add(value.Pos{}, TopName, "",
			"no top file was found in any environment (%v); a highstate needs one", envs)
		return nil, diags
	}

	switch strategy {
	case "same":
		// Each environment uses only its own top file's entries for
		// itself. A top file that names a different environment is an
		// error, because under `same` it can never take effect.
		out := map[string][]string{}
		for env, matched := range perEnv {
			for targetEnv, names := range matched {
				if targetEnv != env {
					diags.Add(value.Pos{File: TopName}, TopName, "",
						"top_file_merging_strategy is `same`, so the top file in %q may not declare states for %q",
						env, targetEnv)
					continue
				}
				out[env] = appendUnique(out[env], names)
			}
		}
		return out, diags

	case "merge_all":
		out := map[string][]string{}
		for _, env := range sortedStringKeys(perEnv) {
			for _, targetEnv := range sortedStringKeys(perEnv[env]) {
				out[targetEnv] = appendUnique(out[targetEnv], perEnv[env][targetEnv])
			}
		}
		return out, diags

	default: // merge
		// The default environment's top file wins where two environments
		// declare the same one; the rest merge in.
		out := map[string][]string{}
		primary := c.env()
		order := append([]string{primary}, sortedStringKeys(perEnv)...)
		seen := map[string]bool{}
		for _, env := range order {
			if seen[env] {
				continue
			}
			seen[env] = true
			matched, ok := perEnv[env]
			if !ok {
				continue
			}
			for _, targetEnv := range sortedStringKeys(matched) {
				out[targetEnv] = appendUnique(out[targetEnv], matched[targetEnv])
			}
		}
		return out, diags
	}
}

// matchTop evaluates one rendered top file against this node.
func (c *Compiler) matchTop(v any, filePath string, diags *Diags) map[string][]string {
	out := map[string][]string{}
	if v == nil {
		return out
	}
	top, ok := v.(*value.Map)
	if !ok {
		diags.Add(value.Pos{File: filePath}, TopName, "",
			"a top file must hold a mapping of environments, found %s", value.TypeName(v))
		return out
	}

	node := target.Node{
		ID:     c.Config.NodeID,
		Grains: c.Config.Grains,
		Pillar: c.Config.Pillar,
	}

	for _, envEntry := range top.Entries() {
		env := value.KeyString(envEntry.Key)
		if env == "include" {
			// A top file may include another; the loader resolves it the
			// same way an SLS include is resolved.
			continue
		}
		targets, ok := envEntry.Val.(*value.Map)
		if !ok {
			diags.Add(envEntry.ValPos, TopName, "",
				"the %q environment in the top file must hold a mapping of target expressions, found %s",
				env, value.TypeName(envEntry.Val))
			continue
		}

		for _, te := range targets.Entries() {
			expr := value.KeyString(te.Key)
			matcher, err := c.compileTopTarget(expr, te.Val)
			if err != nil {
				diags.Add(te.KeyPos, TopName, "", "%v", err)
				continue
			}
			if !matcher.Match(node) {
				continue
			}
			names := topSLSNames(te.Val, te.ValPos, diags)
			out[env] = appendUnique(out[env], names)
		}
	}
	return out
}

// compileTopTarget builds the matcher for one top file entry. Salt allows
// the match type to be given as the first list item, `- match: grain`.
func (c *Compiler) compileTopTarget(expr string, body any) (*target.Matcher, error) {
	kindName := ""
	if items, ok := body.([]any); ok {
		for _, item := range items {
			m, ok := item.(*value.Map)
			if !ok || m.Len() != 1 {
				continue
			}
			e := m.Entries()[0]
			if value.KeyString(e.Key) == "match" {
				kindName = value.KeyString(e.Val)
			}
		}
	}
	if kindName == "" {
		return target.CompileAuto(expr, c.Config.Nodegroups)
	}
	kind, ok := target.KindFromFlag(matchAlias(kindName))
	if !ok {
		return nil, &target.Error{Expr: expr, Msg: "unknown match type " + kindName}
	}
	return target.Compile(kind, expr, c.Config.Nodegroups)
}

// matchAlias maps the long names a top file uses to the flag letters.
func matchAlias(name string) string {
	switch name {
	case "glob":
		return ""
	case "list":
		return "L"
	case "pcre":
		return "E"
	case "grain":
		return "G"
	case "grain_pcre":
		return "P"
	case "pillar":
		return "I"
	case "pillar_pcre":
		return "J"
	case "ipcidr":
		return "S"
	case "nodegroup":
		return "N"
	case "compound":
		return "C"
	}
	return name
}

// topSLSNames reads the SLS list under a matched target, skipping the
// `match` directive.
func topSLSNames(v any, pos value.Pos, diags *Diags) []string {
	items, ok := v.([]any)
	if !ok {
		diags.Add(pos, TopName, "", "a top file target must hold a list of SLS names, found %s", value.TypeName(v))
		return nil
	}
	var out []string
	for _, item := range items {
		switch t := item.(type) {
		case string:
			out = append(out, t)
		case *value.Map:
			// `- match: grain` is a directive, not an SLS name.
			if t.Len() == 1 && value.KeyString(t.Entries()[0].Key) == "match" {
				continue
			}
			diags.Add(pos, TopName, "", "an SLS name must be a string, found a mapping")
		default:
			diags.Add(pos, TopName, "", "an SLS name must be a string, found %s", value.TypeName(item))
		}
	}
	return out
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

func sortedStringKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
