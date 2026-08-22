package template

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// Tuple is a tuple literal's value.
//
// It behaves as a sequence everywhere — iteration, indexing, length,
// membership, and every filter that takes a list — and differs from one
// only in how it renders: `(1, 2)` rather than `[1, 2]`, and `(1,)` for
// the single-element form, which is how Python and Jinja spell it. A tree
// printing a tuple into a file would otherwise write brackets where Salt
// wrote parentheses.
//
// It exists only inside a render. The nine-type model of SPEC section 6.4
// has no tuple, and nothing here puts one into pillar or a state
// argument: by the time a value leaves the engine it is text.
type Tuple []any

// untuple replaces a tuple with a plain slice.
//
// Every path that cares only that a value is a sequence calls this first,
// so a tuple iterates, indexes, unpacks, and filters exactly as a list
// does. Rendering is the one path that must not, since the parentheses
// are the whole difference.
func untuple(v any) any {
	if t, ok := v.(Tuple); ok {
		return []any(t)
	}
	return v
}

// Undefined is the value of a name that does not resolve.
//
// Under strict mode, the default, using one is an error naming the file,
// the line, and the identifier. Under permissive mode it renders as the
// empty string, which is Jinja's behaviour and therefore Salt's, and every
// resolution is logged. The type exists in both modes so that
// `x is defined` and `x | default('y')` behave identically either way.
type Undefined struct {
	Name string
	Pos  Pos
	// Hint explains how the name came to be undefined, such as the
	// attribute that was missing from a defined object.
	Hint string
}

func (u Undefined) String() string { return "" }

func (u Undefined) describe() string {
	if u.Hint != "" {
		return fmt.Sprintf("%s is undefined (%s)", u.Name, u.Hint)
	}
	return fmt.Sprintf("%s is undefined", u.Name)
}

// IsUndefined reports whether a value is the undefined marker.
func IsUndefined(v any) bool { _, ok := v.(Undefined); return ok }

// Callable is anything a template may invoke: a macro, a bound method, a
// module dispatcher entry, or a global function.
type Callable interface {
	Call(args []any, kwargs map[string]any) (any, error)
}

// funcValue adapts a Go function to Callable.
type funcValue struct {
	name string
	fn   func(args []any, kwargs map[string]any) (any, error)
}

func (f funcValue) Call(args []any, kwargs map[string]any) (any, error) {
	return f.fn(args, kwargs)
}

// Dispatcher resolves execution module functions for the `salt` variable
// in a template context. The hub supplies a restricted implementation for
// pillar, reactor, and orchestration rendering; see SPEC section 25.5.
type Dispatcher interface {
	// CallModule invokes `module.function`.
	CallModule(name string, args []any, kwargs map[string]any) (any, error)
	// HasModule reports whether a name is dispatchable, so that
	// `salt['x.y'] is defined` can be answered without calling it.
	HasModule(name string) bool
}

// NewDispatch wraps a Dispatcher as the `salt` context variable.
func NewDispatch(d Dispatcher) any { return dispatchValue{d: d} }

// dispatchValue is the `salt` context variable. It supports both spellings
// SPEC section 10.2.7 requires: salt['cmd.run']('id') and salt.cmd.run('id').
type dispatchValue struct {
	d      Dispatcher
	prefix string
}

func (dv dispatchValue) child(name string) dispatchValue {
	if dv.prefix == "" {
		return dispatchValue{dv.d, name}
	}
	return dispatchValue{dv.d, dv.prefix + "." + name}
}

func (dv dispatchValue) Call(args []any, kwargs map[string]any) (any, error) {
	if dv.prefix == "" {
		return nil, fmt.Errorf("salt must be called as salt['module.function'](...) or salt.module.function(...)")
	}
	return dv.d.CallModule(dv.prefix, args, kwargs)
}

// Namespace backs `{% set ns = namespace(x=1) %}` and `{% set ns.x = 2 %}`,
// which is how a Jinja loop accumulates a value across iterations.
type Namespace struct{ m *value.Map }

func newNamespace() *Namespace { return &Namespace{m: value.NewMap(4)} }

// LoopInfo is the `loop` variable inside a for body.
type LoopInfo struct {
	Index0   int
	Length   int
	Depth0   int
	Items    []any
	cycleIdx int
	prev     any
	changed  any
	hasPrev  bool
	// recurse re-enters the loop body for `{% for %}...recursive`.
	recurse func(items any) (string, error)
}

// ---- attribute and item access ----

// getAttr resolves `obj.name`. Jinja tries the attribute first and falls
// back to a subscript, which is why `grains.os` and `grains['os']` are the
// same thing in an SLS file.
func (r *renderer) getAttr(obj any, name string, pos Pos) (any, error) {
	if u, ok := obj.(Undefined); ok {
		return Undefined{Name: u.Name + "." + name, Pos: pos, Hint: u.Hint}, nil
	}
	switch t := obj.(type) {
	case dispatchValue:
		return t.child(name), nil
	case *Namespace:
		if v, ok := t.m.Get(name); ok {
			return v, nil
		}
		return Undefined{Name: name, Pos: pos, Hint: "not set on this namespace"}, nil
	case *LoopInfo:
		return t.attr(name, pos)
	case *Macro:
		if name == "name" {
			return t.Name, nil
		}
	}

	if m, ok := method(obj, name); ok {
		return m, nil
	}
	v, ok := index(obj, name)
	if ok {
		return v, nil
	}
	// `list.0` is Django's spelling of `list[0]`, which Jinja accepts
	// because its getattr falls through to a subscript. `index` looks up
	// a key in a mapping and a sequence has no key "0", so the
	// fall-through goes to the sequence subscript instead.
	if n, err := strconv.Atoi(name); err == nil {
		switch obj.(type) {
		case []any, Tuple, string:
			return indexSeq(obj, n, pos)
		}
	}
	return Undefined{Name: name, Pos: pos, Hint: fmt.Sprintf("%s has no attribute %q", typeName(obj), name)}, nil
}

// getItem resolves `obj[key]`.
func (r *renderer) getItem(obj, key any, pos Pos) (any, error) {
	if u, ok := obj.(Undefined); ok {
		return Undefined{Name: fmt.Sprintf("%s[%v]", u.Name, key), Pos: pos, Hint: u.Hint}, nil
	}
	if u, ok := key.(Undefined); ok {
		return nil, r.undefinedError(u, pos)
	}
	if dv, ok := obj.(dispatchValue); ok {
		if s, ok := key.(string); ok {
			return dispatchValue{dv.d, s}, nil
		}
	}

	switch k := key.(type) {
	case int64:
		return indexSeq(obj, int(k), pos)
	case int:
		return indexSeq(obj, k, pos)
	}
	if v, ok := index(obj, key); ok {
		return v, nil
	}
	return Undefined{Name: fmt.Sprintf("%v", key), Pos: pos, Hint: fmt.Sprintf("%s has no key %v", typeName(obj), key)}, nil
}

func indexSeq(obj any, i int, pos Pos) (any, error) {
	obj = untuple(obj)

	switch t := obj.(type) {
	case []any:
		if i < 0 {
			i += len(t)
		}
		if i < 0 || i >= len(t) {
			return Undefined{Name: strconv.Itoa(i), Pos: pos, Hint: fmt.Sprintf("index out of range in a sequence of %d", len(t))}, nil
		}
		return t[i], nil
	case string:
		rs := []rune(t)
		if i < 0 {
			i += len(rs)
		}
		if i < 0 || i >= len(rs) {
			return Undefined{Name: strconv.Itoa(i), Pos: pos, Hint: fmt.Sprintf("index out of range in a string of %d", len(rs))}, nil
		}
		return string(rs[i]), nil
	}
	if v, ok := index(obj, int64(i)); ok {
		return v, nil
	}
	return Undefined{Name: strconv.Itoa(i), Pos: pos, Hint: typeName(obj) + " cannot be indexed by a number"}, nil
}

// index looks a key up in any mapping-like value.
func index(obj, key any) (any, bool) {
	switch t := obj.(type) {
	case *value.Map:
		return t.Get(normalizeKey(key))
	case map[string]any:
		s, ok := key.(string)
		if !ok {
			return nil, false
		}
		v, ok := t[s]
		return v, ok
	}
	return nil, false
}

// normalizeKey brings a template-side integer into the key type the
// ordered mapping uses.
func normalizeKey(k any) any {
	if i, ok := k.(int); ok {
		return int64(i)
	}
	return k
}

func (l *LoopInfo) attr(name string, pos Pos) (any, error) {
	switch name {
	case "index":
		return int64(l.Index0 + 1), nil
	case "index0":
		return int64(l.Index0), nil
	case "revindex":
		return int64(l.Length - l.Index0), nil
	case "revindex0":
		return int64(l.Length - l.Index0 - 1), nil
	case "first":
		return l.Index0 == 0, nil
	case "last":
		return l.Index0 == l.Length-1, nil
	case "length":
		return int64(l.Length), nil
	case "depth":
		return int64(l.Depth0 + 1), nil
	case "depth0":
		return int64(l.Depth0), nil
	case "previtem":
		if l.Index0 == 0 {
			return Undefined{Name: "loop.previtem", Pos: pos, Hint: "the first iteration has no previous item"}, nil
		}
		return l.Items[l.Index0-1], nil
	case "nextitem":
		if l.Index0 >= l.Length-1 {
			return Undefined{Name: "loop.nextitem", Pos: pos, Hint: "the last iteration has no next item"}, nil
		}
		return l.Items[l.Index0+1], nil
	case "cycle":
		return funcValue{"loop.cycle", func(args []any, _ map[string]any) (any, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("loop.cycle() needs at least one argument")
			}
			return args[l.Index0%len(args)], nil
		}}, nil
	case "changed":
		return funcValue{"loop.changed", func(args []any, _ map[string]any) (any, error) {
			var cur any
			if len(args) == 1 {
				cur = args[0]
			} else {
				cur = args
			}
			same := l.hasPrev && equalValues(l.changed, cur)
			l.changed, l.hasPrev = cur, true
			return !same, nil
		}}, nil
	case "call":
		if l.recurse == nil {
			return Undefined{Name: "loop.call", Pos: pos, Hint: "this loop is not recursive"}, nil
		}
		return funcValue{"loop", func(args []any, _ map[string]any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("loop() takes exactly one argument")
			}
			return l.recurse(args[0])
		}}, nil
	}
	return Undefined{Name: "loop." + name, Pos: pos, Hint: "the loop variable has no such attribute"}, nil
}

// ---- calling ----

func (r *renderer) callValue(fn any, args []any, kwargs map[string]any, pos Pos) (any, error) {
	switch t := fn.(type) {
	case Undefined:
		return nil, r.undefinedError(t, pos)
	case Callable:
		v, err := t.Call(args, kwargs)
		if err != nil {
			if _, ok := err.(*Error); ok {
				return nil, err
			}
			return nil, &Error{Pos: pos, Msg: err.Error(), Cause: err}
		}
		return v, nil
	case *LoopInfo:
		if t.recurse == nil {
			return nil, errorf(pos, "loop() is available only inside a recursive for loop")
		}
		if len(args) != 1 {
			return nil, errorf(pos, "loop() takes exactly one argument")
		}
		return t.recurse(args[0])
	}
	return nil, errorf(pos, "%s is not callable", typeName(fn))
}

// ---- operators ----

func (r *renderer) binary(op string, l, rv any, pos Pos) (any, error) {
	l, rv = untuple(l), untuple(rv)
	switch op {
	case "~":
		return r.toStr(l, pos) + r.toStr(rv, pos), nil
	case "in":
		return contains(rv, l), nil
	}

	if u, ok := l.(Undefined); ok {
		return nil, r.undefinedError(u, pos)
	}
	if u, ok := rv.(Undefined); ok {
		return nil, r.undefinedError(u, pos)
	}

	switch op {
	case "==":
		return equalValues(l, rv), nil
	case "!=":
		return !equalValues(l, rv), nil
	case "<", "<=", ">", ">=":
		c, err := compare(l, rv)
		if err != nil {
			return nil, &Error{Pos: pos, Msg: err.Error()}
		}
		switch op {
		case "<":
			return c < 0, nil
		case "<=":
			return c <= 0, nil
		case ">":
			return c > 0, nil
		default:
			return c >= 0, nil
		}
	}

	// `+` concatenates strings and lists but raises on mixed types, which
	// is Python's rule and the one that catches a real mistake.
	if op == "+" {
		if ls, ok := l.(string); ok {
			rs, ok := rv.(string)
			if !ok {
				return nil, errorf(pos, "cannot add %s to a string; use ~ to concatenate values of different types", typeName(rv))
			}
			return ls + rs, nil
		}
		if ll, ok := l.([]any); ok {
			rl, ok := rv.([]any)
			if !ok {
				return nil, errorf(pos, "cannot add %s to a list", typeName(rv))
			}
			out := make([]any, 0, len(ll)+len(rl))
			return append(append(out, ll...), rl...), nil
		}
	}

	// String repetition: 'ab' * 3.
	if op == "*" {
		if ls, ok := l.(string); ok {
			n, ok := asInt(rv)
			if !ok {
				return nil, errorf(pos, "a string can only be multiplied by an integer")
			}
			if n < 0 {
				n = 0
			}
			if int64(len(ls))*n > int64(r.opts.MaxOutput) {
				return nil, errorf(pos, "string repetition would exceed the %d byte output limit", r.opts.MaxOutput)
			}
			return strings.Repeat(ls, int(n)), nil
		}
		if ll, ok := l.([]any); ok {
			n, ok := asInt(rv)
			if !ok {
				return nil, errorf(pos, "a list can only be multiplied by an integer")
			}
			out := []any{}
			for i := int64(0); i < n; i++ {
				out = append(out, ll...)
			}
			return out, nil
		}
	}

	return arith(op, l, rv, pos)
}

func arith(op string, l, rv any, pos Pos) (any, error) {
	li, lIsInt := asInt(l)
	ri, rIsInt := asInt(rv)
	lf, lok := asFloat(l)
	rf, rok := asFloat(rv)
	if !lok || !rok {
		return nil, errorf(pos, "cannot apply %s to %s and %s", op, typeName(l), typeName(rv))
	}

	switch op {
	case "+":
		if lIsInt && rIsInt {
			return li + ri, nil
		}
		return lf + rf, nil
	case "-":
		if lIsInt && rIsInt {
			return li - ri, nil
		}
		return lf - rf, nil
	case "*":
		if lIsInt && rIsInt {
			return li * ri, nil
		}
		return lf * rf, nil
	case "/":
		// True division, as in Python 3: 3 / 2 is 1.5.
		if rf == 0 {
			return nil, errorf(pos, "division by zero")
		}
		return lf / rf, nil
	case "//":
		if rf == 0 {
			return nil, errorf(pos, "integer division by zero")
		}
		if lIsInt && rIsInt {
			return int64(math.Floor(float64(li) / float64(ri))), nil
		}
		return math.Floor(lf / rf), nil
	case "%":
		if rf == 0 {
			return nil, errorf(pos, "modulo by zero")
		}
		if lIsInt && rIsInt {
			// Python's modulo takes the sign of the divisor.
			m := li % ri
			if m != 0 && (m < 0) != (ri < 0) {
				m += ri
			}
			return m, nil
		}
		m := math.Mod(lf, rf)
		if m != 0 && (m < 0) != (rf < 0) {
			m += rf
		}
		return m, nil
	case "**":
		if lIsInt && rIsInt && ri >= 0 {
			return int64(math.Pow(float64(li), float64(ri))), nil
		}
		return math.Pow(lf, rf), nil
	}
	return nil, errorf(pos, "unknown operator %s", op)
}

func contains(haystack, needle any) bool {
	haystack, needle = untuple(haystack), untuple(needle)

	switch t := haystack.(type) {
	case string:
		s, ok := needle.(string)
		return ok && strings.Contains(t, s)
	case []any:
		for _, item := range t {
			if equalValues(item, needle) {
				return true
			}
		}
		return false
	case *value.Map:
		return t.Has(normalizeKey(needle))
	case map[string]any:
		s, ok := needle.(string)
		if !ok {
			return false
		}
		_, found := t[s]
		return found
	}
	return false
}

func equalValues(a, b any) bool {
	a, b = untuple(a), untuple(b)

	if ai, ok := asInt(a); ok {
		if bi, ok := asInt(b); ok {
			return ai == bi
		}
	}
	af, aok := asFloat(a)
	bf, bok := asFloat(b)
	if aok && bok {
		return af == bf
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalValues(x[i], y[i]) {
				return false
			}
		}
		return true
	case *value.Map:
		y, ok := b.(*value.Map)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, e := range x.Entries() {
			v, found := y.Get(e.Key)
			if !found || !equalValues(e.Val, v) {
				return false
			}
		}
		return true
	}
	return a == b
}

func compare(a, b any) (int, error) {
	if as, ok := a.(string); ok {
		bs, ok := b.(string)
		if !ok {
			return 0, fmt.Errorf("cannot compare a string with %s", typeName(b))
		}
		return strings.Compare(as, bs), nil
	}
	af, aok := asFloat(a)
	bf, bok := asFloat(b)
	if !aok || !bok {
		return 0, fmt.Errorf("cannot compare %s with %s", typeName(a), typeName(b))
	}
	switch {
	case af < bf:
		return -1, nil
	case af > bf:
		return 1, nil
	default:
		return 0, nil
	}
}

func asInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// truthy follows Python: empty string, empty collection, zero, and none
// are false. SPEC section 10.2.3.
func truthy(v any) bool {
	if _, ok := v.(Undefined); ok {
		return false
	}
	return value.Truthy(v)
}

// ---- iteration ----

// iterate turns a value into the sequence a for loop walks.
func (r *renderer) iterate(v any, pos Pos) ([]any, error) {
	v = untuple(v)

	switch t := v.(type) {
	case Undefined:
		return nil, r.undefinedError(t, pos)
	case []any:
		return t, nil
	case *value.Map:
		// Iterating a mapping yields its keys, in declaration order.
		ks := t.Keys()
		out := make([]any, len(ks))
		for i, k := range ks {
			out[i] = k
		}
		return out, nil
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]any, len(keys))
		for i, k := range keys {
			out[i] = k
		}
		return out, nil
	case string:
		rs := []rune(t)
		out := make([]any, len(rs))
		for i, c := range rs {
			out[i] = string(c)
		}
		return out, nil
	case nil:
		return nil, errorf(pos, "cannot iterate over none")
	}
	return nil, errorf(pos, "cannot iterate over %s", typeName(v))
}

// ---- rendering values to text ----

// toStr renders a value the way `{{ x }}` does.
func (r *renderer) toStr(v any, pos Pos) string {
	s, err := r.toStrErr(v, pos)
	if err != nil {
		return ""
	}
	return s
}

func (r *renderer) toStrErr(v any, pos Pos) (string, error) {
	if u, ok := v.(Undefined); ok {
		if err := r.undefinedError(u, pos); err != nil {
			return "", err
		}
		return "", nil
	}
	return renderValue(v), nil
}

// renderValue is Python's str() for the types a template can hold. A
// collection renders in Python's repr form, because that is what an SLS
// author sees from Jinja and occasionally relies on.
func renderValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) && math.Abs(t) < 1e15 {
			return strconv.FormatFloat(t, 'f', 1, 64)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case []byte:
		return string(t)
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = reprValue(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case Tuple:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = reprValue(item)
		}
		if len(t) == 1 {
			// Python spells a one-element tuple with a trailing comma, to
			// tell it from a parenthesised expression.
			return "(" + parts[0] + ",)"
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case *value.Map:
		parts := make([]string, 0, t.Len())
		for _, e := range t.Entries() {
			parts = append(parts, reprValue(e.Key)+": "+reprValue(e.Val))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case Undefined:
		return ""
	case *Macro:
		return "<macro " + t.Name + ">"
	case fmt.Stringer:
		return t.String()
	}
	return fmt.Sprint(v)
}

// reprValue is Python's repr(): the same as str() except that a string is
// quoted.
func reprValue(v any) string {
	if s, ok := v.(string); ok {
		if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
			return `"` + s + `"`
		}
		return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
	}
	return renderValue(v)
}

func typeName(v any) string {
	switch v.(type) {
	case Undefined:
		return "an undefined value"
	case *Macro:
		return "a macro"
	case dispatchValue:
		return "the salt dispatcher"
	case *Namespace:
		return "a namespace"
	case *LoopInfo:
		return "the loop variable"
	case Callable:
		return "a function"
	}
	return value.TypeName(v)
}
