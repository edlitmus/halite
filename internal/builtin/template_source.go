package builtin

import (
	"fmt"
	"sort"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// dispatchAdapter presents the module registry to a template as `salt`.
type dispatchAdapter struct{ c *exec.Context }

func (d dispatchAdapter) CallModule(name string, args []any, kwargs map[string]any) (any, error) {
	names := make([]string, 0, len(kwargs))
	for k := range kwargs {
		names = append(names, k)
	}
	sort.Strings(names)
	kw := value.NewMap(len(kwargs))
	for _, k := range names {
		kw.Set(k, kwargs[k])
	}
	if d.c.Dispatch == nil {
		return nil, fmt.Errorf("no module dispatcher is available to call %s", name)
	}
	return d.c.Dispatch.CallPositional(d.c, name, args, kw)
}

func (d dispatchAdapter) HasModule(name string) bool {
	return d.c.Dispatch != nil && d.c.Dispatch.Has(name)
}

// renderSourceTemplate applies a state's `template` argument to fetched
// source bytes, as Salt's file states do.
//
// Salt names the engine rather than assuming one, and every engine other
// than jinja is one this build does not have. Naming the unsupported one
// is the difference between "rewrite this line" and "why is my file
// wrong".
func renderSourceTemplate(c *exec.Context, args *value.Map, src []byte, from string) ([]byte, error) {
	engine := states.Str(args, "template", "")
	if engine == "" {
		return src, nil
	}
	if engine != "jinja" {
		return nil, fmt.Errorf("template: %q is not an engine this build has; only jinja is supported. SPEC section 10", engine)
	}

	opts := render.Options{
		File:      from,
		Env:       c.Env,
		PillarEnv: c.Env,
		NodeID:    c.NodeID,
		JobID:     c.JobID,
		Grains:    c.Grains,
		Pillar:    c.Pillar,
		Config:    c.Config,
		Extra:     templateExtras(args),
	}
	if c.Dispatch != nil {
		opts.Salt = dispatchAdapter{c}
	}

	out, warnings, err := render.Template(src, opts)
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		c.Logf("warn", "%s: %s", from, w.Msg)
	}
	return []byte(out), nil
}

// templateExtras builds Salt's `context` and `defaults` arguments into
// the names a rendered file sees, with context winning.
func templateExtras(args *value.Map) map[string]any {
	extras := map[string]any{}
	for _, key := range []string{"defaults", "context"} {
		v, ok := args.Get(key)
		if !ok || v == nil {
			continue
		}
		m, ok := v.(*value.Map)
		if !ok {
			continue
		}
		for _, e := range m.Entries() {
			extras[value.KeyString(e.Key)] = e.Val
		}
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}
