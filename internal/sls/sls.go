// Package sls renders, loads, and compiles Salt-style SLS state files into
// an ordered, requisite-resolved execution plan.
//
// Pipeline: raw file -> Render (text/template with grains) -> yamlite.Parse
// -> compileTree (flatten args, extract includes and requisites) ->
// Loader (resolve includes, merge) -> sortStates (dedup check, topo sort).
package sls

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/edlitmus/halite/internal/yamlite"
)

// Ref is a requisite reference: {Module: "pkg", ID: "install_nginx"}.
// An empty Module matches any state with the given ID.
type Ref struct {
	Module string
	ID     string
}

// State is one compiled state declaration.
type State struct {
	ID     string
	Module string
	Fn     string
	Args   map[string]any
	// BaseID is the declared ID of a state that `names:` expanded into
	// several, so a requisite naming the declaration reaches every one.
	BaseID    string
	Require   []Ref
	Watch     []Ref
	OnChanges []Ref
	Prereq    []Ref
	// The _in forms, resolved onto the states they name before sorting.
	RequireIn   []Ref
	WatchIn     []Ref
	OnChangesIn []Ref
	PrereqIn    []Ref
	Dir         string // directory of the source SLS file (for relative sources)
	Src         string // source SLS path (for error attribution)
}

// Name returns the "module.function" form.
func (s State) Name() string { return s.Module + "." + s.Fn }

// Matches reports whether a requisite reference names this state. A
// reference with no module matches any state with the ID, and a reference
// to a `names:` declaration matches every state it expanded into.
func (s State) Matches(r Ref) bool {
	if r.Module != "" && r.Module != s.Module {
		return false
	}
	return r.ID == s.ID || (s.BaseID != "" && r.ID == s.BaseID)
}

// Ref returns the reference that names this state exactly.
func (s State) Ref() Ref { return Ref{Module: s.Module, ID: s.ID} }

// TemplateData is passed to the template engine when rendering SLS files.
type TemplateData struct {
	Grains map[string]any
	Pillar map[string]any
	// Mine is fleet-wide published facts, function -> agent -> data. It is
	// empty masterless: there is no fleet to gather from.
	Mine map[string]any
}

// templateFuncs are helpers available in SLS templates, covering the most
// common Jinja filters: {{ .Grains.x | default "y" }}, contains, split,
// join, lower, upper, hasPrefix, hasSuffix.
var templateFuncs = template.FuncMap{
	"default": func(def, v any) any {
		if v == nil || v == "" {
			return def
		}
		return v
	},
	"contains":  strings.Contains,
	"hasPrefix": strings.HasPrefix,
	"hasSuffix": strings.HasSuffix,
	"split":     strings.Split,
	"join":      strings.Join,
	"lower":     strings.ToLower,
	"upper":     strings.ToUpper,
}

// TemplateFuncs returns the helper functions available in every halite
// template. Callers that render against something other than TemplateData
// — the reactor, against an event — build their own template with these.
func TemplateFuncs() template.FuncMap { return templateFuncs }

// Render runs src through text/template. Grains and pillar data are
// available as {{ .Grains.os_family }} and {{ .Pillar.nginx.port }}.
// Standard template actions (if/range/with) stand in for Salt's Jinja layer.
func Render(name, src string, data TemplateData) (string, error) {
	t, err := template.New(name).Option("missingkey=zero").Funcs(templateFuncs).Parse(src)
	if err != nil {
		return "", fmt.Errorf("template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("template: %w", err)
	}
	return buf.String(), nil
}

// compileTree turns a parsed SLS tree into unsorted states plus the list of
// included SLS names (from a top-level "include:" key).
func compileTree(root any) (states []State, includes []string, err error) {
	m, ok := root.(*yamlite.Map)
	if !ok {
		return nil, nil, fmt.Errorf("top level of an SLS file must be a mapping of state IDs")
	}
	for _, id := range m.Keys {
		if id == "include" {
			list, ok := m.Vals[id].([]any)
			if !ok {
				return nil, nil, fmt.Errorf("include: must be a list of sls names")
			}
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return nil, nil, fmt.Errorf("include: entries must be sls names, got %v", item)
				}
				includes = append(includes, s)
			}
			continue
		}
		body, ok := m.Vals[id].(*yamlite.Map)
		if !ok {
			return nil, nil, fmt.Errorf("state %q: body must be a mapping of module functions", id)
		}
		for _, fn := range body.Keys {
			parts := strings.SplitN(fn, ".", 2)
			if len(parts) != 2 {
				return nil, nil, fmt.Errorf("state %q: %q is not of the form module.function", id, fn)
			}
			st := State{ID: id, Module: parts[0], Fn: parts[1]}
			if err := flatten(body.Vals[fn], &st); err != nil {
				return nil, nil, fmt.Errorf("state %q (%s): %w", id, fn, err)
			}
			expanded, err := expandNames(st)
			if err != nil {
				return nil, nil, fmt.Errorf("state %q (%s): %w", id, fn, err)
			}
			states = append(states, expanded...)
		}
	}
	return states, includes, nil
}

// flatten converts the Salt arg convention (a list of single-pair maps)
// into a flat args map, pulling out requisites.
func flatten(v any, st *State) error {
	st.Args = map[string]any{}
	add := func(k string, val any) error {
		switch k {
		case "require", "watch", "onchanges", "prereq",
			"require_in", "watch_in", "onchanges_in", "prereq_in":
			r, err := parseRefs(val)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			switch k {
			case "require":
				st.Require = append(st.Require, r...)
			case "watch":
				st.Watch = append(st.Watch, r...)
			case "onchanges":
				st.OnChanges = append(st.OnChanges, r...)
			case "prereq":
				st.Prereq = append(st.Prereq, r...)
			case "require_in":
				st.RequireIn = append(st.RequireIn, r...)
			case "watch_in":
				st.WatchIn = append(st.WatchIn, r...)
			case "onchanges_in":
				st.OnChangesIn = append(st.OnChangesIn, r...)
			case "prereq_in":
				st.PrereqIn = append(st.PrereqIn, r...)
			}
		default:
			st.Args[k] = val
		}
		return nil
	}
	switch t := v.(type) {
	case nil:
	case []any:
		for _, item := range t {
			switch it := item.(type) {
			case *yamlite.Map:
				for _, k := range it.Keys {
					if err := add(k, it.Vals[k]); err != nil {
						return err
					}
				}
			case string:
				// bare flag, e.g. "- makedirs"
				st.Args[it] = "true"
			default:
				return fmt.Errorf("unsupported argument entry %v", item)
			}
		}
	case *yamlite.Map:
		for _, k := range t.Keys {
			if err := add(k, t.Vals[k]); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("arguments must be a list of '- key: value' entries")
	}
	return nil
}

func parseRefs(v any) ([]Ref, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a list")
	}
	var out []Ref
	for _, item := range list {
		switch it := item.(type) {
		case *yamlite.Map:
			for _, k := range it.Keys {
				id, ok := it.Vals[k].(string)
				if !ok {
					return nil, fmt.Errorf("reference %q must map to a state ID", k)
				}
				out = append(out, Ref{Module: k, ID: id})
			}
		case string:
			out = append(out, Ref{ID: it})
		default:
			return nil, fmt.Errorf("unsupported reference %v", item)
		}
	}
	return out, nil
}

// expandNames turns a `names:` declaration into one state per name, each
// carrying that name and the declared ID as its base. Salt's `names` is a
// loop written in the state itself, and a tree that uses it will not
// compile without one.
func expandNames(st State) ([]State, error) {
	raw, declared := st.Args["names"]
	if !declared {
		return []State{st}, nil
	}
	delete(st.Args, "names")
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("names: must be a non-empty list")
	}
	out := make([]State, 0, len(list))
	for _, item := range list {
		name, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("names: entries must be strings, got %v", item)
		}
		expanded := st
		expanded.BaseID = st.ID
		expanded.ID = fmt.Sprintf("%s (%s)", st.ID, name)
		expanded.Args = copyArgs(st.Args)
		expanded.Args["name"] = name
		out = append(out, expanded)
	}
	return out, nil
}

// copyArgs gives each expanded state its own arguments, so that setting a
// name on one does not set it on all of them.
func copyArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// applyInRequisites turns the _in forms into their plain counterparts on
// the states they name: `require_in: [service: nginx]` is "nginx requires
// me", which is the same edge written from the other end. It is also the
// only way to attach a requisite to a state another SLS file declares,
// which is why Salt trees lean on it.
func applyInRequisites(states []State) error {
	type addition struct {
		target int
		kind   string
		ref    Ref
	}
	var additions []addition
	for i, s := range states {
		for _, decl := range []struct {
			kind string
			refs []Ref
		}{
			{"require", s.RequireIn},
			{"watch", s.WatchIn},
			{"onchanges", s.OnChangesIn},
			{"prereq", s.PrereqIn},
		} {
			for _, r := range decl.refs {
				matched := false
				for j, target := range states {
					if i == j || !target.Matches(r) {
						continue
					}
					additions = append(additions, addition{target: j, kind: decl.kind, ref: s.Ref()})
					matched = true
				}
				if !matched {
					return fmt.Errorf("state %q: %s_in target %s:%s not found",
						s.ID, decl.kind, r.Module, r.ID)
				}
			}
		}
	}
	for _, a := range additions {
		target := &states[a.target]
		switch a.kind {
		case "require":
			target.Require = append(target.Require, a.ref)
		case "watch":
			target.Watch = append(target.Watch, a.ref)
		case "onchanges":
			target.OnChanges = append(target.OnChanges, a.ref)
		case "prereq":
			target.Prereq = append(target.Prereq, a.ref)
		}
	}
	return nil
}

// sortStates resolves the _in requisites, checks for duplicate
// declarations, and performs a stable topological sort. require, watch, and
// onchanges are "runs after" edges; prereq reverses the edge: the declaring
// state runs before its target.
func sortStates(in []State) ([]State, error) {
	if err := applyInRequisites(in); err != nil {
		return nil, err
	}
	seen := map[string]string{}
	for _, s := range in {
		key := s.ID + "|" + s.Name()
		if prev, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate state %q (%s) declared in %s and %s",
				s.ID, s.Name(), orDot(prev), orDot(s.Src))
		}
		seen[key] = s.Src
	}
	// A reference to a `names:` declaration names every state it expanded
	// into, so an edge to it is an edge to all of them.
	findAll := func(r Ref) []int {
		var out []int
		for i, s := range in {
			if s.Matches(r) {
				out = append(out, i)
			}
		}
		return out
	}
	deps := make([][]int, len(in))
	for i, s := range in {
		after := append(append(append([]Ref{}, s.Require...), s.Watch...), s.OnChanges...)
		for _, r := range after {
			targets := findAll(r)
			if len(targets) == 0 {
				return nil, fmt.Errorf("state %q: requisite %s:%s not found", s.ID, r.Module, r.ID)
			}
			deps[i] = append(deps[i], targets...)
		}
		for _, r := range s.Prereq {
			targets := findAll(r)
			if len(targets) == 0 {
				return nil, fmt.Errorf("state %q: prereq %s:%s not found", s.ID, r.Module, r.ID)
			}
			for _, j := range targets {
				deps[j] = append(deps[j], i) // target runs after the prereq-declaring state
			}
		}
	}
	done := make([]bool, len(in))
	out := make([]State, 0, len(in))
	for len(out) < len(in) {
		progressed := false
		for i := range in {
			if done[i] {
				continue
			}
			ready := true
			for _, j := range deps[i] {
				if !done[j] {
					ready = false
					break
				}
			}
			if ready {
				done[i] = true
				out = append(out, in[i])
				progressed = true
			}
		}
		if !progressed {
			return nil, fmt.Errorf("requisite cycle detected")
		}
	}
	return out, nil
}

func orDot(s string) string {
	if s == "" {
		return "<input>"
	}
	return s
}
