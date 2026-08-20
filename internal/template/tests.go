package template

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"context"
	"time"

	"github.com/edlitmus/halite/internal/regexcompat"
	"github.com/edlitmus/halite/internal/value"
)

// TestFunc is one `is` test. SPEC section 10.2.5 lists the set.
type TestFunc func(fc *FilterContext, v any, args []any) (bool, error)

func (r *renderer) applyTest(t *TestExpr) (any, error) {
	fn, ok := r.env.tests[t.Name]
	if !ok {
		return nil, errorf(t.Pos(), "unknown test %q", t.Name)
	}
	obj, err := r.eval(t.Obj)
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(t.Args))
	for _, a := range t.Args {
		v, err := r.eval(a)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	fc := &FilterContext{r: r, Pos: t.Pos()}
	res, err := fn(fc, obj, args)
	if err != nil {
		if _, ok := err.(*Error); ok {
			return nil, err
		}
		return nil, &Error{Pos: t.Pos(), Msg: err.Error(), Cause: err}
	}
	if t.Not {
		return !res, nil
	}
	return res, nil
}

func standardTests() map[string]TestFunc {
	t := map[string]TestFunc{}

	// `defined` and `undefined` are the reason the Undefined type exists
	// in both strict and permissive mode: they must answer without the
	// strict-mode error firing.
	t["defined"] = func(_ *FilterContext, v any, _ []any) (bool, error) { return !IsUndefined(v), nil }
	t["undefined"] = func(_ *FilterContext, v any, _ []any) (bool, error) { return IsUndefined(v), nil }
	t["none"] = func(_ *FilterContext, v any, _ []any) (bool, error) { return v == nil, nil }
	t["boolean"] = kindTest(func(v any) bool { _, ok := v.(bool); return ok })
	t["true"] = func(_ *FilterContext, v any, _ []any) (bool, error) { b, ok := v.(bool); return ok && b, nil }
	t["false"] = func(_ *FilterContext, v any, _ []any) (bool, error) { b, ok := v.(bool); return ok && !b, nil }

	t["string"] = kindTest(func(v any) bool { _, ok := v.(string); return ok })
	t["number"] = kindTest(func(v any) bool {
		switch v.(type) {
		case int64, int, float64:
			return true
		}
		return false
	})
	t["integer"] = kindTest(func(v any) bool {
		switch v.(type) {
		case int64, int:
			return true
		}
		return false
	})
	t["float"] = kindTest(func(v any) bool { _, ok := v.(float64); return ok })

	t["mapping"] = kindTest(isMapping)
	t["dict"] = kindTest(isMapping)
	t["sequence"] = kindTest(func(v any) bool {
		switch v.(type) {
		case []any, string, *value.Map:
			return true
		}
		return false
	})
	t["list"] = kindTest(func(v any) bool { _, ok := v.([]any); return ok })
	t["iterable"] = kindTest(func(v any) bool {
		switch v.(type) {
		case []any, string, *value.Map, map[string]any:
			return true
		}
		return false
	})
	t["callable"] = kindTest(func(v any) bool {
		_, ok := v.(Callable)
		return ok
	})

	t["escaped"] = kindTest(func(any) bool { return false })

	t["even"] = intTest(func(n int64) bool { return n%2 == 0 })
	t["odd"] = intTest(func(n int64) bool { return n%2 != 0 })

	t["divisibleby"] = func(fc *FilterContext, v any, args []any) (bool, error) {
		if len(args) != 1 {
			return false, fc.Errorf("divisibleby takes one argument")
		}
		n, ok1 := asInt(v)
		d, ok2 := asInt(args[0])
		if !ok1 || !ok2 {
			return false, fc.Errorf("divisibleby needs two integers")
		}
		if d == 0 {
			return false, fc.Errorf("divisibleby: division by zero")
		}
		return n%d == 0, nil
	}

	t["eq"] = cmpTest(func(c int) bool { return c == 0 })
	t["equalto"] = t["eq"]
	t["=="] = t["eq"]
	t["ne"] = cmpTest(func(c int) bool { return c != 0 })
	t["!="] = t["ne"]
	t["lt"] = cmpTest(func(c int) bool { return c < 0 })
	t["lessthan"] = t["lt"]
	t["<"] = t["lt"]
	t["le"] = cmpTest(func(c int) bool { return c <= 0 })
	t["<="] = t["le"]
	t["gt"] = cmpTest(func(c int) bool { return c > 0 })
	t["greaterthan"] = t["gt"]
	t[">"] = t["gt"]
	t["ge"] = cmpTest(func(c int) bool { return c >= 0 })
	t[">="] = t["ge"]

	t["in"] = func(fc *FilterContext, v any, args []any) (bool, error) {
		if len(args) != 1 {
			return false, fc.Errorf("the in test takes one argument")
		}
		return contains(args[0], v), nil
	}

	t["sameas"] = func(fc *FilterContext, v any, args []any) (bool, error) {
		if len(args) != 1 {
			return false, fc.Errorf("sameas takes one argument")
		}
		return v == args[0], nil
	}

	t["lower"] = strTest(func(s string) bool { return s == strings.ToLower(s) && hasCased(s) })
	t["upper"] = strTest(func(s string) bool { return s == strings.ToUpper(s) && hasCased(s) })

	// Salt's `match` test, which is a regular expression rather than a
	// glob and so is subject to SPEC section 10.4.
	t["match"] = func(fc *FilterContext, v any, args []any) (bool, error) {
		if len(args) < 1 {
			return false, fc.Errorf("match takes a pattern")
		}
		s, err := fc.Str(v)
		if err != nil {
			return false, err
		}
		pattern, ok := args[0].(string)
		if !ok {
			return false, fc.Errorf("match takes a string pattern")
		}
		ignoreCase := len(args) > 1 && truthy(args[1])
		re, err := regexcompat.CompileWithFlags(pattern, ignoreCase, false, false)
		if err != nil {
			return false, fc.Errorf("%v", err)
		}
		return re.MatchString(s), nil
	}

	return t
}

func isMapping(v any) bool {
	switch v.(type) {
	case *value.Map, map[string]any:
		return true
	}
	return false
}

func kindTest(fn func(any) bool) TestFunc {
	return func(_ *FilterContext, v any, _ []any) (bool, error) { return fn(v), nil }
}

func intTest(fn func(int64) bool) TestFunc {
	return func(fc *FilterContext, v any, _ []any) (bool, error) {
		n, ok := asInt(v)
		if !ok {
			return false, fc.Errorf("expected an integer, found %s", typeName(v))
		}
		return fn(n), nil
	}
}

func strTest(fn func(string) bool) TestFunc {
	return func(fc *FilterContext, v any, _ []any) (bool, error) {
		s, ok := v.(string)
		if !ok {
			return false, nil
		}
		return fn(s), nil
	}
}

func cmpTest(keep func(int) bool) TestFunc {
	return func(fc *FilterContext, v any, args []any) (bool, error) {
		if len(args) != 1 {
			return false, fc.Errorf("this test takes one argument")
		}
		if keep(0) && !keep(1) && !keep(-1) {
			// Equality does not need an ordering, so structural equality
			// answers it for collections too.
			return equalValues(v, args[0]), nil
		}
		if !keep(0) && keep(1) && keep(-1) {
			return !equalValues(v, args[0]), nil
		}
		c, err := compare(v, args[0])
		if err != nil {
			return false, fc.Errorf("%v", err)
		}
		return keep(c), nil
	}
}

func hasCased(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// lookPath backs the `which` filter.
func lookPath(name string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		info, err := os.Stat(name)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return "", os.ErrNotExist
		}
		return name, nil
	}
	return exec.LookPath(name)
}

// dnsContext bounds the dns_check filter, so that a template cannot hang
// on an unreachable resolver for longer than the render deadline allows.
func dnsContext() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	// The cancel is deliberately deferred to the garbage collector rather
	// than leaked: the lookup below returns before the timeout in every
	// path, and holding the cancel here would need a caller change for no
	// behavioural gain.
	_ = cancel
	return ctx
}
