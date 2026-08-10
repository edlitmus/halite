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
	ID        string
	Module    string
	Fn        string
	Args      map[string]any
	Require   []Ref
	Watch     []Ref
	OnChanges []Ref
	Prereq    []Ref
	Dir       string // directory of the source SLS file (for relative sources)
	Src       string // source SLS path (for error attribution)
}

// Name returns the "module.function" form.
func (s State) Name() string { return s.Module + "." + s.Fn }

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
			states = append(states, st)
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
		case "require", "watch", "onchanges", "prereq":
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

// sortStates checks for duplicate declarations and performs a stable
// topological sort. require, watch, and onchanges are "runs after" edges;
// prereq reverses the edge: the declaring state runs before its target.
func sortStates(in []State) ([]State, error) {
	seen := map[string]string{}
	for _, s := range in {
		key := s.ID + "|" + s.Name()
		if prev, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate state %q (%s) declared in %s and %s",
				s.ID, s.Name(), orDot(prev), orDot(s.Src))
		}
		seen[key] = s.Src
	}
	find := func(r Ref) (int, bool) {
		for i, s := range in {
			if s.ID == r.ID && (r.Module == "" || r.Module == s.Module) {
				return i, true
			}
		}
		return 0, false
	}
	deps := make([][]int, len(in))
	for i, s := range in {
		after := append(append(append([]Ref{}, s.Require...), s.Watch...), s.OnChanges...)
		for _, r := range after {
			j, ok := find(r)
			if !ok {
				return nil, fmt.Errorf("state %q: requisite %s:%s not found", s.ID, r.Module, r.ID)
			}
			deps[i] = append(deps[i], j)
		}
		for _, r := range s.Prereq {
			j, ok := find(r)
			if !ok {
				return nil, fmt.Errorf("state %q: prereq %s:%s not found", s.ID, r.Module, r.ID)
			}
			deps[j] = append(deps[j], i) // target runs after the prereq-declaring state
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
