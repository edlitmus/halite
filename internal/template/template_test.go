package template

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// mapLoader serves templates from memory, for include, import, and
// extends tests.
type mapLoader map[string]string

func (m mapLoader) Load(name string) (string, string, error) {
	src, ok := m[name]
	if !ok {
		return "", "", ErrNotFound
	}
	return src, name, nil
}

func render(t *testing.T, src string, ctx map[string]any) string {
	t.Helper()
	return renderWith(t, src, ctx, nil, nil)
}

func renderWith(t *testing.T, src string, ctx map[string]any, loader Loader, tweak func(*Options)) string {
	t.Helper()
	opts := DefaultOptions()
	if tweak != nil {
		tweak(&opts)
	}
	env := NewEnvironment(loader, opts)
	res, err := env.RenderString(src, "t.sls", ctx)
	if err != nil {
		t.Fatalf("render of %q failed: %v", src, err)
	}
	return res.Output
}

func renderErr(t *testing.T, src string, ctx map[string]any) error {
	t.Helper()
	env := NewEnvironment(nil, DefaultOptions())
	_, err := env.RenderString(src, "t.sls", ctx)
	if err == nil {
		t.Fatalf("expected %q to fail", src)
	}
	return err
}

func mustContain(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("got %q, which does not contain %q", got, want)
	}
}

func TestOutputAndLiterals(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ 1 + 2 }}`, "3"},
		{`{{ 'a' ~ 'b' }}`, "ab"},
		{`{{ 'a' ~ 1 }}`, "a1"},
		{`{{ 7 / 2 }}`, "3.5"},
		{`{{ 7 // 2 }}`, "3"},
		{`{{ -7 // 2 }}`, "-4"},
		{`{{ 7 % 3 }}`, "1"},
		{`{{ -7 % 3 }}`, "2"},
		{`{{ 2 ** 10 }}`, "1024"},
		{`{{ 2 ** 3 ** 2 }}`, "512"},
		{`{{ 'ab' * 3 }}`, "ababab"},
		{`{{ [1,2] + [3] }}`, "[1, 2, 3]"},
		{`{{ true }}`, "True"},
		{`{{ none }}`, "None"},
		{`{{ 1.5 }}`, "1.5"},
		{`{{ 3.0 }}`, "3.0"},
		{`{{ not 0 }}`, "True"},
		{`{{ 1 if true else 2 }}`, "1"},
		{`{{ 1 if false else 2 }}`, "2"},
		{`{{ 'x' in 'axb' }}`, "True"},
		{`{{ 3 not in [1,2] }}`, "True"},
		{`{{ {'a': 1}['a'] }}`, "1"},
		{`{{ (1, 2)[1] }}`, "2"},
		{`{{ [1,2,3][-1] }}`, "3"},
		{`{{ [1,2,3,4,5][1:4] }}`, "[2, 3, 4]"},
		{`{{ [1,2,3,4,5][::2] }}`, "[1, 3, 5]"},
		{`{{ 'abcdef'[1:4] }}`, "bcd"},
		{`{{ 'abc'[::-1] }}`, "cba"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestOperatorPrecedence(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ 1 + 2 * 3 }}`, "7"},
		{`{{ (1 + 2) * 3 }}`, "9"},
		{`{{ 2 + 3 == 5 }}`, "True"},
		{`{{ true or false and false }}`, "True"},
		{`{{ not true and false }}`, "False"},
		{`{{ -2 ** 2 }}`, "-4"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestIfElifElse(t *testing.T) {
	src := `{% if n == 1 %}one{% elif n == 2 %}two{% else %}many{% endif %}`
	for n, want := range map[int64]string{1: "one", 2: "two", 9: "many"} {
		if got := render(t, src, map[string]any{"n": n}); got != want {
			t.Errorf("n=%d -> %q, want %q", n, got, want)
		}
	}
}

func TestForLoopVariables(t *testing.T) {
	src := `{% for x in items %}{{ loop.index }}:{{ x }}{% if not loop.last %},{% endif %}{% endfor %}`
	got := render(t, src, map[string]any{"items": []any{"a", "b", "c"}})
	if got != "1:a,2:b,3:c" {
		t.Errorf("got %q", got)
	}
}

func TestForLoopFullVariableSet(t *testing.T) {
	src := `{% for x in items %}[{{ loop.index0 }} {{ loop.revindex }} {{ loop.revindex0 }} ` +
		`{{ loop.first }} {{ loop.last }} {{ loop.length }} {{ loop.depth }}]{% endfor %}`
	got := render(t, src, map[string]any{"items": []any{"a", "b"}})
	want := "[0 2 1 True False 2 1][1 1 0 False True 2 1]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestForPrevAndNextItem(t *testing.T) {
	src := `{% for x in items %}{% if not loop.first %}{{ loop.previtem }}<{% endif %}{{ x }}{% if not loop.last %}>{{ loop.nextitem }} {% endif %}{% endfor %}`
	got := render(t, src, map[string]any{"items": []any{"a", "b", "c"}})
	if got != "a>b a<b>c b<c" {
		t.Errorf("got %q", got)
	}
}

func TestLoopCycleAndChanged(t *testing.T) {
	src := `{% for x in items %}{{ loop.cycle('odd','even') }}{% endfor %}`
	if got := render(t, src, map[string]any{"items": []any{1, 2, 3}}); got != "oddevenodd" {
		t.Errorf("cycle: %q", got)
	}
	src = `{% for x in items %}{{ loop.changed(x) }}{% endfor %}`
	if got := render(t, src, map[string]any{"items": []any{"a", "a", "b"}}); got != "TrueFalseTrue" {
		t.Errorf("changed: %q", got)
	}
}

func TestForElseOnEmpty(t *testing.T) {
	src := `{% for x in items %}{{ x }}{% else %}nothing{% endfor %}`
	if got := render(t, src, map[string]any{"items": []any{}}); got != "nothing" {
		t.Errorf("got %q", got)
	}
}

func TestForTupleUnpacking(t *testing.T) {
	src := `{% for k, v in pairs %}{{ k }}={{ v }};{% endfor %}`
	pairs := []any{[]any{"a", int64(1)}, []any{"b", int64(2)}}
	if got := render(t, src, map[string]any{"pairs": pairs}); got != "a=1;b=2;" {
		t.Errorf("got %q", got)
	}
}

func TestForOverMappingItems(t *testing.T) {
	m := value.MapOf("z", int64(1), "a", int64(2))
	src := `{% for k, v in m.items() %}{{ k }}{{ v }}{% endfor %}`
	// Declaration order, not sorted order.
	if got := render(t, src, map[string]any{"m": m}); got != "z1a2" {
		t.Errorf("got %q, want declaration order", got)
	}
}

func TestForInlineCondition(t *testing.T) {
	src := `{% for x in items if x > 2 %}{{ x }}/{{ loop.length }} {% endfor %}`
	items := []any{int64(1), int64(2), int64(3), int64(4)}
	if got := render(t, src, map[string]any{"items": items}); got != "3/2 4/2 " {
		t.Errorf("got %q", got)
	}
}

func TestRecursiveLoop(t *testing.T) {
	tree := []any{
		value.MapOf("name", "a", "children", []any{
			value.MapOf("name", "b", "children", []any{}),
		}),
	}
	src := `{% for node in tree recursive %}{{ loop.depth }}{{ node.name }}{{ loop(node.children) }}{% endfor %}`
	if got := render(t, src, map[string]any{"tree": tree}); got != "1a2b" {
		t.Errorf("got %q", got)
	}
}

func TestSetAndNamespace(t *testing.T) {
	if got := render(t, `{% set x = 5 %}{{ x }}`, nil); got != "5" {
		t.Errorf("set: %q", got)
	}
	src := `{% set ns = namespace(total=0) %}{% for x in items %}{% set ns.total = ns.total + x %}{% endfor %}{{ ns.total }}`
	items := []any{int64(1), int64(2), int64(3)}
	if got := render(t, src, map[string]any{"items": items}); got != "6" {
		t.Errorf("namespace accumulation: %q", got)
	}
}

func TestBlockSet(t *testing.T) {
	src := `{% set greeting %}hello {{ who }}{% endset %}{{ greeting }}|{{ greeting | upper }}`
	if got := render(t, src, map[string]any{"who": "world"}); got != "hello world|HELLO WORLD" {
		t.Errorf("got %q", got)
	}
}

func TestBlockSetWithFilter(t *testing.T) {
	src := `{% set x | upper %}abc{% endset %}{{ x }}`
	if got := render(t, src, nil); got != "ABC" {
		t.Errorf("got %q", got)
	}
}

func TestMacros(t *testing.T) {
	src := `{% macro greet(name, greeting='hello') %}{{ greeting }}, {{ name }}!{% endmacro %}` +
		`{{ greet('world') }} {{ greet('bob', greeting='hi') }}`
	if got := render(t, src, nil); got != "hello, world! hi, bob!" {
		t.Errorf("got %q", got)
	}
}

func TestMacroVarargsAndKwargs(t *testing.T) {
	src := `{% macro m(a) %}{{ a }}|{{ varargs }}|{{ kwargs['z'] }}{% endmacro %}{{ m(1, 2, 3, z=9) }}`
	if got := render(t, src, nil); got != "1|[2, 3]|9" {
		t.Errorf("got %q", got)
	}
}

func TestCallAndCaller(t *testing.T) {
	src := `{% macro wrap() %}[{{ caller() }}]{% endmacro %}{% call wrap() %}inside{% endcall %}`
	if got := render(t, src, nil); got != "[inside]" {
		t.Errorf("got %q", got)
	}
}

func TestCallWithParameters(t *testing.T) {
	src := `{% macro each(items) %}{% for i in items %}{{ caller(i) }}{% endfor %}{% endmacro %}` +
		`{% call(item) each([1,2]) %}<{{ item }}>{% endcall %}`
	if got := render(t, src, nil); got != "<1><2>" {
		t.Errorf("got %q", got)
	}
}

func TestFilterBlock(t *testing.T) {
	if got := render(t, `{% filter upper %}abc{% endfilter %}`, nil); got != "ABC" {
		t.Errorf("got %q", got)
	}
}

func TestDoAndWith(t *testing.T) {
	src := `{% set ns = namespace(n=0) %}{% do 1 + 1 %}{% with a = 3 %}{{ a }}{% endwith %}{{ a is defined }}`
	if got := render(t, src, nil); got != "3False" {
		t.Errorf("got %q", got)
	}
}

func TestRawBlock(t *testing.T) {
	src := `{% raw %}{{ not_evaluated }}{% if x %}{% endraw %}`
	if got := render(t, src, nil); got != "{{ not_evaluated }}{% if x %}" {
		t.Errorf("got %q", got)
	}
}

func TestComments(t *testing.T) {
	if got := render(t, `a{# this is dropped #}b`, nil); got != "ab" {
		t.Errorf("got %q", got)
	}
}

func TestWhitespaceControl(t *testing.T) {
	cases := []struct{ src, want string }{
		{"a  {{- 'x' }}  b", "ax  b"},
		{"a  {{ 'x' -}}  b", "a  xb"},
		{"a\n{%- if true -%}\n  b\n{%- endif -%}\nc", "abc"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%q -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestIncludeImportExtends(t *testing.T) {
	loader := mapLoader{
		"partial.jinja": "included {{ who }}",
		"macros.jinja":  `{% macro m(x) %}<{{ x }}>{% endmacro %}{% set const = 7 %}`,
		"base.jinja":    `head[{% block body %}base body{% endblock %}]tail`,
	}
	ctx := map[string]any{"who": "world"}

	if got := renderWith(t, `{% include 'partial.jinja' %}`, ctx, loader, nil); got != "included world" {
		t.Errorf("include: %q", got)
	}
	if got := renderWith(t, `{% include 'missing.jinja' ignore missing %}ok`, ctx, loader, nil); got != "ok" {
		t.Errorf("ignore missing: %q", got)
	}
	if got := renderWith(t, `{% import 'macros.jinja' as mm %}{{ mm.m(1) }}{{ mm.const }}`, ctx, loader, nil); got != "<1>7" {
		t.Errorf("import: %q", got)
	}
	if got := renderWith(t, `{% from 'macros.jinja' import m as mac %}{{ mac(2) }}`, ctx, loader, nil); got != "<2>" {
		t.Errorf("from import: %q", got)
	}
	if got := renderWith(t, `{% extends 'base.jinja' %}{% block body %}child{% endblock %}`, ctx, loader, nil); got != "head[child]tail" {
		t.Errorf("extends: %q", got)
	}
	if got := renderWith(t, `{% extends 'base.jinja' %}{% block body %}[{{ super() }}]{% endblock %}`, ctx, loader, nil); got != "head[[base body]]tail" {
		t.Errorf("super: %q", got)
	}
}

func TestIncludeWithoutContext(t *testing.T) {
	loader := mapLoader{"p.jinja": `{{ who is defined }}`}
	if got := renderWith(t, `{% include 'p.jinja' without context %}`, map[string]any{"who": "x"}, loader, nil); got != "False" {
		t.Errorf("without context: %q", got)
	}
}

// ---- undefined behaviour, SPEC section 10.2.6 ----

func TestStrictUndefinedNamesFileLineAndIdentifier(t *testing.T) {
	err := renderErr(t, "line one\n{{ missing_value }}\n", nil)
	msg := err.Error()
	for _, want := range []string{"t.sls", ":2:", "missing_value", "undefined"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestStrictUndefinedOnMissingAttribute(t *testing.T) {
	err := renderErr(t, `{{ pillar.nope }}`, map[string]any{"pillar": value.NewMap(0)})
	mustContain(t, err.Error(), "nope")
}

func TestPermissiveUndefinedRendersEmptyAndReports(t *testing.T) {
	var seen []string
	got := renderWith(t, `[{{ missing }}]`, nil, nil, func(o *Options) {
		o.Undefined = Permissive
		o.OnUndefined = func(name string, pos Pos) { seen = append(seen, name) }
	})
	if got != "[]" {
		t.Errorf("permissive render = %q, want []", got)
	}
	if len(seen) != 1 || seen[0] != "missing" {
		t.Errorf("OnUndefined saw %v, want [missing]", seen)
	}
}

func TestOptionalValueSpellingsWorkInBothModes(t *testing.T) {
	pillar := value.MapOf("a", value.MapOf("b", "found"))
	cases := []struct{ src, want string }{
		{`{{ nope | default('y') }}`, "y"},
		{`{% if nope is defined %}yes{% else %}no{% endif %}`, "no"},
		{`{{ pillar.get('a:b', 'fallback') }}`, "found"},
		{`{{ pillar.get('a:zz', 'fallback') }}`, "fallback"},
	}
	for _, mode := range []UndefinedMode{Strict, Permissive} {
		for _, c := range cases {
			got := renderWith(t, c.src, map[string]any{"pillar": pillar}, nil, func(o *Options) { o.Undefined = mode })
			if got != c.want {
				t.Errorf("mode %v: %s -> %q, want %q", mode, c.src, got, c.want)
			}
		}
	}
}

func TestShortCircuitProtectsUndefined(t *testing.T) {
	if got := render(t, `{{ 'yes' if x is defined and x.y else 'no' }}`, nil); got != "no" {
		t.Errorf("got %q", got)
	}
}

// ---- limits, SPEC section 10.2.8 ----

func TestLoopIterationLimit(t *testing.T) {
	env := NewEnvironment(nil, func() Options {
		o := DefaultOptions()
		o.MaxIterations = 100
		return o
	}())
	_, err := env.RenderString(`{% for i in range(1000) %}x{% endfor %}`, "t.sls", nil)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected an iteration limit error, got %v", err)
	}
}

func TestOutputSizeLimit(t *testing.T) {
	env := NewEnvironment(nil, func() Options {
		o := DefaultOptions()
		o.MaxOutput = 64
		return o
	}())
	_, err := env.RenderString(`{% for i in range(1000) %}xxxxxxxxxx{% endfor %}`, "t.sls", nil)
	if err == nil || !strings.Contains(err.Error(), "output exceeded") {
		t.Fatalf("expected an output limit error, got %v", err)
	}
}

func TestIncludeDepthLimit(t *testing.T) {
	loader := mapLoader{"self.jinja": `{% include 'self.jinja' %}`}
	env := NewEnvironment(loader, DefaultOptions())
	_, err := env.RenderString(`{% include 'self.jinja' %}`, "t.sls", nil)
	if err == nil || !strings.Contains(err.Error(), "deeper than") {
		t.Fatalf("expected a depth error, got %v", err)
	}
}

func TestDivisionByZeroNamesThePosition(t *testing.T) {
	err := renderErr(t, "ok\n{{ 1 / 0 }}", nil)
	mustContain(t, err.Error(), "division by zero")
	mustContain(t, err.Error(), ":2:")
}

func TestPlusRefusesMixedTypes(t *testing.T) {
	err := renderErr(t, `{{ 'a' + 1 }}`, nil)
	mustContain(t, err.Error(), "~")
}

// ---- determinism, SPEC section 10.2.4 ----

func TestRandomIsDeterministicPerSeed(t *testing.T) {
	src := `{{ [1,2,3,4,5,6,7,8,9,10] | random }}{{ range(100) | shuffle | first }}`
	seedA := func(o *Options) { o.RandomSeed = "web1.prod|20260819T142211" }
	first := renderWith(t, src, nil, nil, seedA)
	second := renderWith(t, src, nil, nil, seedA)
	if first != second {
		t.Errorf("the same seed produced %q then %q; a test run and the real run must agree", first, second)
	}
	other := renderWith(t, src, nil, nil, func(o *Options) { o.RandomSeed = "web2.prod|20260819T142211" })
	if other == first {
		t.Errorf("a different node produced the same value %q", first)
	}
}

// ---- source map, SPEC section 10.1.4 ----

func TestSourceMapPointsAtTheTemplateLine(t *testing.T) {
	src := "one\n{% for i in range(3) %}\nitem {{ i }}\n{% endfor %}\nlast\n"
	env := NewEnvironment(nil, DefaultOptions())
	res, err := env.RenderString(src, "web.sls", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The rendered output has more lines than the source; a position for
	// each of them must resolve back into the source.
	lines := strings.Count(res.Output, "\n")
	if lines < 4 {
		t.Fatalf("unexpected output:\n%s", res.Output)
	}
	pos, ok := res.MapLine(lines)
	if !ok {
		t.Fatal("no source map entry for the last output line")
	}
	if pos.File != "web.sls" || pos.Line == 0 {
		t.Errorf("source map gave %v", pos)
	}
}

func TestSyntaxErrorsCarryPositions(t *testing.T) {
	cases := []struct{ src, want string }{
		{"ok\n{{ 1 + }}", "unexpected"},
		{"{% if true %}no end", "expected"},
		{"{% nosuchtag %}", "unknown tag"},
		{"{{ 1 | nosuchfilter }}", "unknown filter"},
		{"{{ 1 is nosuchtest }}", "unknown test"},
		{"{% trans %}{% endtrans %}", "not supported"},
	}
	for _, c := range cases {
		err := renderErr(t, c.src, nil)
		mustContain(t, err.Error(), c.want)
	}
}

func TestEndblockNameMustMatch(t *testing.T) {
	err := renderErr(t, `{% block a %}x{% endblock b %}`, nil)
	mustContain(t, err.Error(), "does not match")
}
