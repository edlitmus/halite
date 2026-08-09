// Package sls renders and compiles Salt-style SLS state files into an
// ordered, requisite-resolved execution plan.
//
// Pipeline: raw file -> Render (text/template with grains) -> yamlite.Parse
// -> Compile (flatten args, extract requisites, topological sort).
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
	ID      string
	Module  string
	Fn      string
	Args    map[string]any
	Require []Ref
	Watch   []Ref
}

// Name returns the "module.function" form.
func (s State) Name() string { return s.Module + "." + s.Fn }

// TemplateData is passed to the template engine when rendering SLS files.
type TemplateData struct {
	Grains map[string]any
}

// Render runs src through text/template. Grains are available as
// {{ .Grains.os_family }} etc. Standard template actions (if/range/with)
// stand in for Salt's Jinja layer.
func Render(name, src string, data TemplateData) (string, error) {
	t, err := template.New(name).Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", fmt.Errorf("template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("template: %w", err)
	}
	return buf.String(), nil
}

// Compile turns a parsed SLS tree into an ordered list of states with
// requisites resolved. Declaration order is preserved except where
// require/watch edges force reordering.
func Compile(root any) ([]State, error) {
	m, ok := root.(*yamlite.Map)
	if !ok {
		return nil, fmt.Errorf("top level of an SLS file must be a mapping of state IDs")
	}
	var states []State
	for _, id := range m.Keys {
		body, ok := m.Vals[id].(*yamlite.Map)
		if !ok {
			return nil, fmt.Errorf("state %q: body must be a mapping of module functions", id)
		}
		for _, fn := range body.Keys {
			parts := strings.SplitN(fn, ".", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("state %q: %q is not of the form module.function", id, fn)
			}
			args, req, watch, err := flatten(body.Vals[fn])
			if err != nil {
				return nil, fmt.Errorf("state %q (%s): %w", id, fn, err)
			}
			states = append(states, State{
				ID: id, Module: parts[0], Fn: parts[1],
				Args: args, Require: req, Watch: watch,
			})
		}
	}
	return sortStates(states)
}

// flatten converts the Salt arg convention (a list of single-pair maps) into
// a flat args map, pulling out require/watch requisites.
func flatten(v any) (args map[string]any, req, watch []Ref, err error) {
	args = map[string]any{}
	add := func(k string, val any) error {
		switch k {
		case "require":
			r, err := parseRefs(val)
			if err != nil {
				return fmt.Errorf("require: %w", err)
			}
			req = append(req, r...)
		case "watch":
			r, err := parseRefs(val)
			if err != nil {
				return fmt.Errorf("watch: %w", err)
			}
			watch = append(watch, r...)
		default:
			args[k] = val
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
						return nil, nil, nil, err
					}
				}
			case string:
				// bare flag, e.g. "- makedirs"
				args[it] = "true"
			default:
				return nil, nil, nil, fmt.Errorf("unsupported argument entry %v", item)
			}
		}
	case *yamlite.Map:
		for _, k := range t.Keys {
			if err := add(k, t.Vals[k]); err != nil {
				return nil, nil, nil, err
			}
		}
	default:
		return nil, nil, nil, fmt.Errorf("arguments must be a list of '- key: value' entries")
	}
	return args, req, watch, nil
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

// sortStates performs a stable topological sort over require+watch edges.
func sortStates(in []State) ([]State, error) {
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
		refs := append(append([]Ref{}, s.Require...), s.Watch...)
		for _, r := range refs {
			j, ok := find(r)
			if !ok {
				return nil, fmt.Errorf("state %q: requisite %s:%s not found", s.ID, r.Module, r.ID)
			}
			deps[i] = append(deps[i], j)
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
