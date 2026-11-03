package template

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// These tests hold measureValue (shape.go), the check made before every
// recursive walk of a value. The deep and cyclic shapes are built in a
// child process with a capped stack, as limits_test.go does for the
// parser, because on code without the check they overflow the stack, and
// in this process that would be the 1 GiB default. Only values that the
// old code could walk safely, if wrongly, are built here.

// nestedList is a list nested depth deep around a zero.
func nestedList(depth int) any {
	var v any = int64(0)
	for range depth {
		v = []any{v}
	}
	return v
}

// encodingDispatcher stands for an execution module that encodes its
// arguments, as anything that sends them on does.
type encodingDispatcher struct{ called int }

func (d *encodingDispatcher) CallModule(name string, args []any, kwargs map[string]any) (any, error) {
	d.called++
	b, err := value.EncodeJSON(args, 0)
	if err != nil {
		return nil, err
	}
	n := len(b)
	for _, v := range kwargs {
		b, err := value.EncodeJSON(v, 0)
		if err != nil {
			return nil, err
		}
		n += len(b)
	}
	return fmt.Sprintf("%s: %d bytes", name, n), nil
}

func (d *encodingDispatcher) HasModule(string) bool { return true }

// walks is every way a template hands a value to a recursive walk, each
// with the value as x.
var walks = map[string]string{
	"print":            `{{ x }}`,
	"concat":           `{{ x ~ '' }}`,
	"string":           `{{ x | string }}`,
	"pprint":           `{{ x | pprint }}`,
	"tojson":           `{{ x | tojson }}`,
	"tojson indent":    `{{ x | tojson(indent=1) }}`,
	"json":             `{{ x | json }}`,
	"yaml flow":        `{{ x | yaml }}`,
	"yaml block":       `{{ x | yaml(False) }}`,
	"yaml_encode":      `{{ x | yaml_encode }}`,
	"equal":            `{{ x == x }}`,
	"not equal":        `{{ x != [x] }}`,
	"less":             `{{ x < [x] }}`,
	"eq test":          `{{ x is eq(x) }}`,
	"in":               `{{ x in [x] }}`,
	"in test":          `{{ x is in([x]) }}`,
	"join":             `{{ [x] | join }}`,
	"join method":      `{{ ''.join([x]) }}`,
	"unique":           `{{ [x, x] | unique | list | length }}`,
	"sort":             `{{ [x, [x]] | sort | length }}`,
	"min":              `{{ [x, [x]] | min | length }}`,
	"union":            `{{ [x] | union([x]) | length }}`,
	"flatten":          `{{ [x] | flatten | length }}`,
	"format filter":    `{{ '%s' | format(x) }}`,
	"format method":    `{{ '{}'.format(x) }}`,
	"list count":       `{{ [x].count(x) }}`,
	"list index":       `{{ [x].index(x) }}`,
	"list remove":      `{% set l = [x] %}{% do l.remove(x) %}{{ l | length }}`,
	"loop changed":     `{% for i in [1, 2] %}{{ loop.changed(x) }}{% endfor %}`,
	"set_dict_key":     `{{ {'a': x} | set_dict_key_value('b', 1) | length }}`,
	"salt argument":    `{{ salt['test.echo'](x) }}`,
	"salt keyword":     `{{ salt['test.echo'](a=x) }}`,
	"dictsort":         `{{ {'a': x} | dictsort | length }}`,
	"xmlattr":          `{{ {'a': x} | xmlattr }}`,
	"groupby":          `{{ [{'a': x}] | groupby('a') | length }}`,
	"dict_to_sls_yaml": `{{ {'a': x} | dict_to_sls_yaml_params }}`,
	// A key that is not a scalar is spelt for the mapping's index by
	// fmt.Sprint, which recurses as deep as the key does.
	"dict key":   `{{ {x: 1} | length }}`,
	"subscript":  `{{ {'a': 1}[x] is defined }}`,
	"in mapping": `{{ x in {'a': 1} }}`,
	"dict get":   `{{ {'a': 1}.get(x) }}`,
	"dict pop":   `{{ {'a': 1}.pop(x, 0) }}`,
	"setdefault": `{% set m = {} %}{{ m.setdefault(x, 1) }}`,
	"list key":   `{{ [1][x] is defined }}`,
	"dict pairs": `{{ dict([[x, 1]]) | length }}`,
}

func walkEnv(d Dispatcher) (*Environment, map[string]any) {
	return NewEnvironment(nil, DefaultOptions()), map[string]any{"salt": NewDispatch(d)}
}

// TestDeepValuesAreRefusedBeforeTheyAreWalked builds the value in Go,
// one level past the limit, so that the old code walks it on this
// process's stack without harm (a few megabytes) and the test fails
// there by rendering it.
func TestDeepValuesAreRefusedBeforeTheyAreWalked(t *testing.T) {
	past := nestedList(maxValueDepth + 1)
	for name, src := range walks {
		t.Run(name, func(t *testing.T) {
			d := &encodingDispatcher{}
			env, ctx := walkEnv(d)
			ctx["x"] = past
			res, err := env.RenderString(src, "t.sls", ctx)
			var te *Error
			if !errors.As(err, &te) || te.Pos.Line == 0 || !strings.Contains(te.Msg, "nests deeper than") {
				t.Fatalf("want a positioned depth error, got %d bytes, %v", len(res.Output), err)
			}
			if d.called != 0 {
				t.Fatalf("the module was called with the value")
			}
		})
	}
}

// atLimit is as deep as x may be for every walk in walks: some of them
// put it inside two more containers before walking it.
const atLimit = maxValueDepth - 2

// TestValuesAtTheDepthLimitAreWalked is the other edge: no walk refuses
// a value as deep as the limit allows, and printing, which adds no
// containers, takes exactly maxValueDepth and refuses one more.
func TestValuesAtTheDepthLimitAreWalked(t *testing.T) {
	env := NewEnvironment(nil, DefaultOptions())
	if _, err := env.RenderString(`{{ x }}`, "t.sls", map[string]any{"x": nestedList(maxValueDepth)}); err != nil {
		t.Fatalf("printing at the limit: %v", err)
	}
	if _, err := env.RenderString(`{{ x }}`, "t.sls", map[string]any{"x": nestedList(maxValueDepth + 1)}); err == nil {
		t.Fatalf("printing past the limit was not refused")
	}
	at := nestedList(atLimit)
	for name, src := range walks {
		t.Run(name, func(t *testing.T) {
			env, ctx := walkEnv(&encodingDispatcher{})
			ctx["x"] = at
			if _, err := env.RenderString(src, "t.sls", ctx); err != nil && strings.Contains(err.Error(), "nests deeper") {
				t.Fatalf("refused at the limit: %v", err)
			}
		})
	}
}

// TestSharedValuesAreMeasuredByWhatTheyRenderTo covers the value that
// is small in memory and large when walked. Each pass of the loop holds
// one more list and doubles the items a walk visits, so twenty passes is
// a million leaves. On the old code each of these walked all of them
// and built the result, a few megabytes against a limit of 1,000.
func TestSharedValuesAreMeasuredByWhatTheyRenderTo(t *testing.T) {
	build := `{% set ns = namespace(x=0, y=1) %}{% for i in range(20) %}{% set ns.x = [ns.x, ns.x] %}{% set ns.y = [ns.y, ns.y] %}{% endfor %}`
	refused := map[string]string{
		"string":  `{{ (ns.x | string) | length }}`,
		"tojson":  `{{ (ns.x | tojson) | length }}`,
		"yaml":    `{{ (ns.x | yaml(False)) | length }}`,
		"equal":   `{{ ns.x == ns.y }}`,
		"join":    `{{ ([ns.x] | join) | length }}`,
		"in":      `{{ ns.y in [ns.x] }}`,
		"concat":  `{{ (ns.x ~ '') | length }}`,
		"pprint":  `{{ (ns.x | pprint) | length }}`,
		"element": `{{ ('%s' | format(ns.x)) | length }}`,
	}
	for name, src := range refused {
		t.Run(name, func(t *testing.T) {
			out, err := renderSmall(build + src)
			var te *Error
			if !errors.As(err, &te) || te.Pos.Line == 0 || !strings.Contains(te.Msg, "would render to more than") {
				t.Fatalf("want a positioned size error, got %q, %v", out, err)
			}
		})
	}
	// Under the limit the same shapes render as before.
	small := `{% set ns = namespace(x=0) %}{% for i in range(3) %}{% set ns.x = [ns.x, ns.x] %}{% endfor %}`
	if out, err := renderSmall(small + `{{ ns.x }}`); err != nil || out != "[[[0, 0], [0, 0]], [[0, 0], [0, 0]]]" {
		t.Fatalf("got %q, %v", out, err)
	}
}

// TestIndentationIsChargedBeforeEncoding: the encoders pad each item by
// the indent for every container around it, which no measure of the
// contents sees. The indents here are small, so that the old code built
// each result and the test failed on it by measuring it.
func TestIndentationIsChargedBeforeEncoding(t *testing.T) {
	refused := map[string]string{
		"tojson":     `{{ ([1] | tojson(indent=2000)) | length }}`,
		"json":       `{{ ({'a': 1} | json(indent=2000)) | length }}`,
		"yaml block": `{% set ns = namespace(x=0) %}{% for i in range(40) %}{% set ns.x = {'a': ns.x} %}{% endfor %}{{ (ns.x | yaml(False)) | length }}`,
	}
	for name, src := range refused {
		t.Run(name, func(t *testing.T) {
			out, err := renderSmall(src)
			var te *Error
			if !errors.As(err, &te) || te.Pos.Line == 0 {
				t.Fatalf("want a positioned error, got %q, %v", out, err)
			}
		})
	}
	allowed := map[string][2]string{
		"tojson":     {`{{ [1] | tojson(indent=2) }}`, "[\n  1\n]"},
		"scalar":     {`{{ 1 | tojson(indent=900) }}`, "1"},
		"yaml block": {`{{ {'a': {'b': 1}} | yaml(False) }}`, "a:\n  b: 1"},
	}
	for name, c := range allowed {
		t.Run(name, func(t *testing.T) {
			out, err := renderSmall(c[0])
			if err != nil || out != c[1] {
				t.Fatalf("%s = %q, %v; want %q", c[0], out, err, c[1])
			}
		})
	}
}

// TestHugeIndentIsRefusedBeforeEncoding calls the check directly, with
// an indent that would be ten gigabytes of spaces for one item. Through
// a template the result-size check after encoding would refuse it too,
// but only after the allocation, so this is the one case that has to be
// caught before it, and a test that rendered it would, on code without
// the check, allocate it.
func TestHugeIndentIsRefusedBeforeEncoding(t *testing.T) {
	r := newRenderer(NewEnvironment(nil, DefaultOptions()), &budget{})
	fc := &FilterContext{r: r, Pos: Pos{File: "t.sls", Line: 1}}
	if err := checkIndented(fc, []any{int64(1)}, 10_000_000_000, "tojson"); err == nil {
		t.Fatalf("an indent of ten billion was not refused")
	}
	if err := checkIndented(fc, []any{int64(1)}, 2, "tojson"); err != nil {
		t.Fatalf("an indent of two: %v", err)
	}
	// A scalar and an empty list are never padded, whatever the indent.
	for _, v := range []any{int64(1), []any{}} {
		if err := checkIndented(fc, v, 10_000_000_000, "tojson"); err != nil {
			t.Fatalf("%v: %v", v, err)
		}
	}
}

// TestComparisonsThatDoNotRecurseDoNotMeasure: equality of values of
// different types or lengths, ordering of anything but two lists, and a
// lookup in a mapping are answered without walking the values, so they
// do not measure them. The mapping here could not be rendered within the
// small environment's limit, so measuring it would refuse each of these.
func TestComparisonsThatDoNotRecurseDoNotMeasure(t *testing.T) {
	big := strings.Repeat("x", 900)
	ctx := map[string]any{
		"m": value.MapOf("a", big, "b", big),
		"l": []any{big, big},
	}
	cases := map[string]string{
		`{{ 'a' in m }}`:          "True",
		`{{ 'a' is in(m) }}`:      "True",
		`{{ m == {} }}`:           "False",
		`{{ m != 1 }}`:            "True",
		`{{ m is eq({'a': 1}) }}`: "False",
		`{{ l == [1] }}`:          "False",
		`{{ l != m }}`:            "True",
	}
	for src, want := range cases {
		res, err := smallEnv().RenderString(src, "t.sls", ctx)
		if err != nil || res.Output != want {
			t.Errorf("%s = %q, %v; want %q", src, res.Output, err, want)
		}
	}
	// The same shapes, when they would recurse, are measured.
	for _, src := range []string{`{{ l == [big, big] }}`, `{{ big in l }}`, `{{ l < [1, 2] }}`} {
		ctx["big"] = big
		if _, err := smallEnv().RenderString(src, "t.sls", ctx); err == nil || !strings.Contains(err.Error(), "would render to more than") {
			t.Errorf("%s: want a size error, got %v", src, err)
		}
	}
}

// TestComparingDeepListsReportsOneElement: a mismatch at the bottom of
// two deep lists used to be wrapped once per level.
func TestComparingDeepListsReportsOneElement(t *testing.T) {
	env := NewEnvironment(nil, DefaultOptions())
	var a, b any = "s", int64(1)
	for range 50 {
		a, b = []any{a}, []any{b}
	}
	_, err := env.RenderString(`{{ a < b }}`, "t.sls", map[string]any{"a": a, "b": b})
	if err == nil || strings.Count(err.Error(), "comparing element") != 1 {
		t.Fatalf("want one element named, got %v", err)
	}
}

// ---- in a child, with a capped stack ----

const valueHelperEnv = "HALITE_TEMPLATE_VALUE_HELPER"

// deepBuild makes ns.x a list nested 200,000 deep, and cycle makes d a
// mapping that holds itself. Both are what a template can do with
// nothing but a loop and a method; on the old code every walk below
// overflowed childStack on them.
const (
	deepBuild  = `{% set ns = namespace(x=0) %}{% for i in range(200000) %}{% set ns.x = [ns.x] %}{% endfor %}{% set x = ns.x %}`
	cycleBuild = `{% set x = {} %}{% do x.update({'x': x}) %}`
)

// valueHelper is the child: it renders one walk of one built value, and
// the parent reads what it printed.
func TestValueStackHelper(t *testing.T) {
	spec := os.Getenv(valueHelperEnv)
	if spec == "" {
		t.Skip("child process for TestDeepValuesDoNotOverflowTheStack; runs only when that test starts it")
	}
	kind, walk, _ := strings.Cut(spec, "/")
	src, ok := walks[walk]
	if !ok {
		t.Fatalf("no walk %q", walk)
	}
	debug.SetMaxStack(childStack)
	env, ctx := walkEnv(&encodingDispatcher{})
	switch kind {
	case "deep":
		src = deepBuild + src
	case "cycle":
		src = cycleBuild + src
	case "at-limit":
		// The deepest value that may be walked, under the tallest
		// statement nesting the parser allows, which is what the
		// stack has to hold at once.
		ctx["x"] = nestedList(atLimit)
		src = strings.Repeat("{% if 1 %}", 99) + src + strings.Repeat("{% endif %}", 99)
	default:
		t.Fatalf("no kind %q", kind)
	}
	_, err := env.RenderString(src, "t.sls", ctx)
	if kind == "at-limit" {
		if err != nil && strings.Contains(err.Error(), "nests deeper") {
			t.Fatalf("refused at the limit: %v", err)
		}
		fmt.Println("SURVIVED: walked")
		return
	}
	if answeredAtTheTop[spec] {
		var te *Error
		if err != nil && (!errors.As(err, &te) || strings.Contains(te.Msg, "internal error")) {
			t.Fatalf("want an ordinary result, got %v", err)
		}
		fmt.Println("SURVIVED: answered without walking:", err)
		return
	}
	var te *Error
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "nests deeper than") {
		t.Fatalf("want a depth error, got %v", err)
	}
	fmt.Println("SURVIVED: refused:", te.Msg)
}

// answeredAtTheTop are the walks of a mapping that holds itself that do
// not recurse, because a mapping and a list are unequal and unordered
// without looking inside either, so they are not measured or refused.
var answeredAtTheTop = map[string]bool{
	"cycle/less":      true,
	"cycle/not equal": true,
}

func TestDeepValuesDoNotOverflowTheStack(t *testing.T) {
	if os.Getenv(valueHelperEnv) != "" || os.Getenv(stackHelperEnv) != "" {
		t.Skip("in the child")
	}
	if testing.Short() {
		t.Skip("starts a child process per walk")
	}
	names := make([]string, 0, len(walks))
	for n := range walks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, kind := range []string{"deep", "cycle", "at-limit"} {
		for _, name := range names {
			t.Run(kind+"/"+name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestValueStackHelper$", "-test.count=1", "-test.v")
				cmd.Env = append(os.Environ(), valueHelperEnv+"="+kind+"/"+name)
				out, err := cmd.CombinedOutput()
				if err != nil || !strings.Contains(string(out), "SURVIVED") {
					s := string(out)
					if i := strings.Index(s, "fatal error"); i >= 0 {
						s = s[i:min(len(s), i+600)]
					} else if len(s) > 2000 {
						s = s[len(s)-2000:]
					}
					t.Fatalf("%s did not survive (%v):\n%s", kind+"/"+name, err, s)
				}
			})
		}
	}
}
