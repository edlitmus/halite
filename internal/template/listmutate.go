package template

import (
	"fmt"

	"github.com/edlitmus/halite/internal/value"
)

// Python's mutating list methods.
//
// Jinja allows them because a Jinja list is a Python list, and a Salt
// tree leans on that: the shape is `{% set items = [] %}` followed by
// `{% do items.append(x) %}` inside a loop or a macro, because `set`
// inside a loop body does not survive the loop and mutating the object
// does. The estate's own tree does this in six files.
//
// A Go []any is not a Python list. Appending to it produces a new slice
// header, and nothing that held the old one can see the change, so the
// method alone is not enough -- the call site has to put the result
// back where the receiver came from. listReference is that half, and
// the two only work together. SPEC section 10.2.
//
// What this does not reproduce is aliasing. Two names bound to one
// Python list both see an append through either; here only the name the
// call was written against is updated. A tree that aliases a list and
// mutates it through one name is the case this cannot follow, and it is
// recorded in DIVERGENCE 5.107 rather than papered over.

// listMutator applies one mutating method, returning the new list and
// whatever the method evaluates to.
type listMutator func(r *renderer, pos Pos, l []any, args []any, kwargs map[string]any) ([]any, any, error)

func mutatingListMethod(name string) (listMutator, bool) {
	switch name {
	case "append":
		return func(_ *renderer, _ Pos, l []any, args []any, _ map[string]any) ([]any, any, error) {
			if len(args) != 1 {
				return l, nil, fmt.Errorf("append() takes one argument")
			}
			return append(append([]any{}, l...), args[0]), nil, nil
		}, true

	case "extend":
		return func(r *renderer, pos Pos, l []any, args []any, _ map[string]any) ([]any, any, error) {
			if len(args) != 1 {
				return l, nil, fmt.Errorf("extend() takes one argument")
			}
			// The same conversion a `for` uses, so extend accepts
			// exactly what the template can already loop over.
			items, err := r.iterate(args[0], pos)
			if err != nil {
				return l, nil, fmt.Errorf("extend() takes a sequence, found %s", typeName(args[0]))
			}
			return append(append([]any{}, l...), items...), nil, nil
		}, true

	case "insert":
		return func(_ *renderer, _ Pos, l []any, args []any, _ map[string]any) ([]any, any, error) {
			if len(args) != 2 {
				return l, nil, fmt.Errorf("insert() takes an index and a value")
			}
			i, err := intArg(args[0], "insert")
			if err != nil {
				return l, nil, err
			}
			// Python clamps rather than failing: insert(-1) goes before
			// the last item, insert(999) appends.
			if i < 0 {
				i += len(l)
				if i < 0 {
					i = 0
				}
			}
			if i > len(l) {
				i = len(l)
			}
			out := make([]any, 0, len(l)+1)
			out = append(out, l[:i]...)
			out = append(out, args[1])
			out = append(out, l[i:]...)
			return out, nil, nil
		}, true

	case "remove":
		return func(_ *renderer, _ Pos, l []any, args []any, _ map[string]any) ([]any, any, error) {
			if len(args) != 1 {
				return l, nil, fmt.Errorf("remove() takes one argument")
			}
			for i, item := range l {
				if equalValues(item, args[0]) {
					out := make([]any, 0, len(l)-1)
					out = append(out, l[:i]...)
					out = append(out, l[i+1:]...)
					return out, nil, nil
				}
			}
			return l, nil, fmt.Errorf("remove(): value is not in the list")
		}, true

	case "pop":
		return func(_ *renderer, _ Pos, l []any, args []any, _ map[string]any) ([]any, any, error) {
			if len(l) == 0 {
				return l, nil, fmt.Errorf("pop(): the list is empty")
			}
			i := len(l) - 1
			if len(args) > 0 {
				n, err := intArg(args[0], "pop")
				if err != nil {
					return l, nil, err
				}
				i = n
				if i < 0 {
					i += len(l)
				}
			}
			if i < 0 || i >= len(l) {
				return l, nil, fmt.Errorf("pop(): index %d is out of range", i)
			}
			got := l[i]
			out := make([]any, 0, len(l)-1)
			out = append(out, l[:i]...)
			out = append(out, l[i+1:]...)
			return out, got, nil
		}, true

	case "clear":
		return func(_ *renderer, _ Pos, l []any, _ []any, _ map[string]any) ([]any, any, error) {
			return []any{}, nil, nil
		}, true

	case "reverse":
		return func(_ *renderer, _ Pos, l []any, _ []any, _ map[string]any) ([]any, any, error) {
			out := make([]any, len(l))
			for i, item := range l {
				out[len(l)-1-i] = item
			}
			return out, nil, nil
		}, true

	case "sort":
		return func(_ *renderer, _ Pos, l []any, _ []any, kwargs map[string]any) ([]any, any, error) {
			out := append([]any{}, l...)
			reverse := false
			if v, ok := kwargs["reverse"]; ok {
				reverse = truthy(v)
			}
			// The `sort` filter's ordering, so `items|sort` and
			// `items.sort()` cannot disagree about what sorted means.
			sortAny(out, reverse, true, nil)
			return out, nil, nil
		}, true
	}
	return nil, false
}

func intArg(v any, who string) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	}
	return 0, fmt.Errorf("%s() needs an integer index, found %s", who, typeName(v))
}

// listReference is a place a list can be read from and written back to.
//
// The setter is what makes a mutating method visible past the
// expression it was written in. A receiver with no setter -- a literal,
// a function's return value -- is mutated and discarded, which is what
// Python does with a temporary too.
type listReference struct {
	get func() (any, error)
	set func(any) bool
}

// listRefFor builds a reference for a receiver expression, evaluating
// any container exactly once so that `f().items.append(x)` does not call
// `f` twice.
func (r *renderer) listRefFor(e Expr) (listReference, error) {
	switch t := e.(type) {
	case *NameExpr:
		return listReference{
			get: func() (any, error) {
				v, _ := r.scope.lookup(t.Name)
				return v, nil
			},
			set: func(v any) bool { return r.scope.assign(t.Name, v) },
		}, nil

	case *AttrExpr:
		obj, err := r.eval(t.Obj)
		if err != nil {
			return listReference{}, err
		}
		return listReference{
			get: func() (any, error) { return r.getAttr(obj, t.Attr, t.Pos()) },
			set: func(v any) bool { return setMember(obj, t.Attr, v) },
		}, nil

	case *ItemExpr:
		obj, err := r.eval(t.Obj)
		if err != nil {
			return listReference{}, err
		}
		key, err := r.eval(t.Index)
		if err != nil {
			return listReference{}, err
		}
		return listReference{
			get: func() (any, error) { return r.getItem(obj, key, t.Pos()) },
			set: func(v any) bool { return setMember(obj, key, v) },
		}, nil
	}

	// Not a place: evaluate it and accept that the change goes nowhere.
	return listReference{
		get: func() (any, error) { return r.eval(e) },
		set: func(any) bool { return false },
	}, nil
}

// setMember writes a value into a container.
//
// A map and a slice element are both shared through the value that
// holds them, so writing here is visible to everything holding that
// container -- which is why a list nested in pillar or in another list
// behaves the way Python's would.
func setMember(obj any, key any, v any) bool {
	switch o := obj.(type) {
	case *value.Map:
		o.Set(normalizeKey(key), v)
		return true
	case *Namespace:
		o.m.Set(normalizeKey(key), v)
		return true
	case []any:
		i, err := intArg(normalizeKey(key), "index")
		if err != nil {
			return false
		}
		if i < 0 {
			i += len(o)
		}
		if i < 0 || i >= len(o) {
			return false
		}
		o[i] = v
		return true
	}
	return false
}
