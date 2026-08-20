// Package states is the state module surface.
//
// The test-mode contract of SPEC section 11.6 is a first-class part of it,
// not a suggestion: in test mode a function must make no change, return a
// nil result when it would change something, populate changes with the
// predicted change, and populate comment with a human sentence.
// Conformance is enforced by the shared harness in this package, which
// every state module must pass. Salt has no such harness, which is why
// test=True in Salt is unreliable for a nontrivial fraction of modules.
package states

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// Result is what a state function returns.
//
// Result is a pointer so that the third state a run can be in has a
// representation: true succeeded, false failed, and nil means "test mode,
// and this would have changed something".
type Result struct {
	Name     string
	Result   *bool
	Changes  *value.Map
	Comment  string
	Warnings []string
}

// True marks a success.
func True(comment string) Result {
	t := true
	return Result{Result: &t, Comment: comment, Changes: value.NewMap(0)}
}

// False marks a failure.
func False(comment string) Result {
	f := false
	return Result{Result: &f, Comment: comment, Changes: value.NewMap(0)}
}

// Changed marks a success that made changes.
func Changed(comment string, changes *value.Map) Result {
	t := true
	if changes == nil {
		changes = value.NewMap(0)
	}
	return Result{Result: &t, Comment: comment, Changes: changes}
}

// WouldChange is the test-mode return: no change was made, and this is
// what would have changed.
func WouldChange(comment string, changes *value.Map) Result {
	if changes == nil {
		changes = value.NewMap(0)
	}
	return Result{Result: nil, Comment: comment, Changes: changes}
}

// Change builds the {old, new} pair Salt's changes mapping uses.
func Change(old, new any) *value.Map {
	return value.MapOf("old", old, "new", new)
}

// HasChanges reports whether the result carries any change.
func (r Result) HasChanges() bool { return r.Changes != nil && r.Changes.Len() > 0 }

// Succeeded reports whether the result is a success. A nil result, which
// is test mode's "would change", counts as a success for the purpose of
// requisites, because the state did not fail.
func (r Result) Succeeded() bool { return r.Result == nil || *r.Result }

// Failed reports whether the result is an explicit failure.
func (r Result) Failed() bool { return r.Result != nil && !*r.Result }

// ResultString renders the result the way an operator reads it.
func (r Result) ResultString() string {
	switch {
	case r.Result == nil:
		return "would change"
	case *r.Result:
		return "succeeded"
	default:
		return "failed"
	}
}

// Func is one state function.
type Func func(c *exec.Context, args *value.Map) (Result, error)

// Module is a state function together with its signature and, where the
// module supports `watch`, its mod_watch reaction.
type Module struct {
	Sig signature.Signature
	Fn  Func
	// ModWatch runs when a watch requisite fires, in place of the normal
	// function. A module without one falls back to running normally,
	// which is what Salt does.
	ModWatch Func
}

// Registry holds the state modules a build ships.
type Registry struct {
	fns  map[string]Module
	sigs *signature.Registry
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{fns: map[string]Module{}, sigs: signature.NewRegistry()}
}

// Add registers state modules.
func (r *Registry) Add(mods ...Module) {
	for _, m := range mods {
		r.fns[m.Sig.Name()] = m
		r.sigs.Add(m.Sig)
	}
}

// Signatures exposes the signature registry, which the state compiler
// validates against.
func (r *Registry) Signatures() *signature.Registry { return r.sigs }

// Lookup finds a state module function.
func (r *Registry) Lookup(name string) (Module, bool) {
	m, ok := r.fns[name]
	return m, ok
}

// Has reports whether a function is registered.
func (r *Registry) Has(name string) bool { _, ok := r.fns[name]; return ok }

// Names lists every state function, sorted.
func (r *Registry) Names() []string { return r.sigs.Names() }

// Modules lists the distinct state module names.
func (r *Registry) Modules() []string { return r.sigs.Modules() }

// Call binds arguments and invokes the function.
func (r *Registry) Call(c *exec.Context, name string, args *value.Map) (Result, error) {
	m, ok := r.fns[name]
	if !ok {
		return Result{}, fmt.Errorf("%q is not a state function this build ships", name)
	}
	bound, errs := m.Sig.Bind(nil, args)
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return Result{}, fmt.Errorf("%s: %s", name, strings.Join(msgs, "; "))
	}
	return m.Fn(c, bound)
}

// CallWatch invokes a module's mod_watch reaction, falling back to the
// normal function when the module has none.
func (r *Registry) CallWatch(c *exec.Context, name string, args *value.Map) (Result, error) {
	m, ok := r.fns[name]
	if !ok {
		return Result{}, fmt.Errorf("%q is not a state function this build ships", name)
	}
	fn := m.ModWatch
	if fn == nil {
		fn = m.Fn
	}
	bound, errs := m.Sig.Bind(nil, args)
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return Result{}, fmt.Errorf("%s: %s", name, strings.Join(msgs, "; "))
	}
	return fn(c, bound)
}

// SupportsWatch reports whether a module defines a mod_watch reaction, so
// that a `watch` on a module without one can be reported rather than
// silently behaving as `require`.
func (r *Registry) SupportsWatch(name string) bool {
	m, ok := r.fns[name]
	return ok && m.ModWatch != nil
}

// arg reads a bound argument.
func arg(args *value.Map, name string) (any, bool) { return args.Get(name) }

// Str reads a string argument, with a default.
func Str(args *value.Map, name, def string) string {
	v, ok := arg(args, name)
	if !ok || v == nil {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return value.KeyString(v)
}

// Bool reads a boolean argument, with a default.
func Bool(args *value.Map, name string, def bool) bool {
	v, ok := arg(args, name)
	if !ok || v == nil {
		return def
	}
	return value.Truthy(v)
}

// Int reads an integer argument, with a default.
func Int(args *value.Map, name string, def int64) int64 {
	v, ok := arg(args, name)
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	}
	return def
}

// Strings reads a list-of-strings argument, accepting a bare string as a
// one-item list.
func Strings(args *value.Map, name string) []string {
	v, ok := arg(args, name)
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, value.KeyString(item))
		}
		return out
	}
	return nil
}

// Mapping reads a mapping argument.
func Mapping(args *value.Map, name string) *value.Map {
	v, ok := arg(args, name)
	if !ok {
		return nil
	}
	m, _ := v.(*value.Map)
	return m
}

// SortedNames renders a set of names for a comment sentence.
func SortedNames(in []string) string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return strings.Join(out, ", ")
}
