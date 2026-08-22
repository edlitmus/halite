package exec

import (
	"sort"

	"github.com/edlitmus/halite/internal/value"
)

// TemplateDispatcher adapts a registry and a context to the `salt` object
// a template sees, so that `salt['pillar.get']('a:b')` in an SLS file
// calls the same function `halite-node call pillar.get a:b` does.
//
// It satisfies template.Dispatcher structurally rather than by importing
// the template package, which would make the dependency run the wrong
// way: the template engine knows nothing about modules.
type TemplateDispatcher struct {
	Registry *Registry
	Context  *Context
}

// CallModule invokes `module.function` with Salt's argument convention.
func (d TemplateDispatcher) CallModule(name string, args []any, kwargs map[string]any) (any, error) {
	// Go's map iteration order is random and the bound arguments are an
	// ordered map, so the keys are sorted: two renders of the same
	// template must produce the same thing.
	names := make([]string, 0, len(kwargs))
	for k := range kwargs {
		names = append(names, k)
	}
	sort.Strings(names)
	kw := value.NewMap(len(kwargs))
	for _, k := range names {
		kw.Set(k, kwargs[k])
	}
	return d.Registry.CallPositional(d.Context, name, args, kw)
}

// HasModule reports whether a name can be dispatched, so that
// `salt['x.y'] is defined` can be answered without calling it.
func (d TemplateDispatcher) HasModule(name string) bool { return d.Registry.Has(name) }
