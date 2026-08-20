package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// These tests reach the paths the behavioural suite does not: the loader,
// the extension points, the error renderings, and the filters whose only
// caller is an SLS tree nobody has written yet. SPEC section 31 holds the
// template engine to the same bar as the parser, for the same reason: it
// is where a defect becomes a wrong change on a host.

func TestDirLoaderResolvesAndContains(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "part.jinja"), []byte("included"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside.jinja")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)

	l := DirLoader{Root: root}
	src, name, err := l.Load("sub/part.jinja")
	if err != nil {
		t.Fatal(err)
	}
	if src != "included" || name != "sub/part.jinja" {
		t.Errorf("src=%q name=%q", src, name)
	}

	// A traversal is refused rather than served, which is the same
	// property the file server guarantees.
	if _, _, err := l.Load("../outside.jinja"); err == nil {
		t.Error("a traversing template name must be refused")
	}
	if _, _, err := l.Load("nope.jinja"); err == nil {
		t.Error("a missing template must be an error")
	}
}

func TestDirLoaderBacksAnInclude(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "p.jinja"), []byte("from disk: {{ who }}"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewEnvironment(DirLoader{Root: root}, DefaultOptions())
	res, err := env.RenderString(`{% include 'p.jinja' %}`, "t.sls", map[string]any{"who": "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "from disk: ops" {
		t.Errorf("output = %q", res.Output)
	}
}

func TestCustomFilterAndTest(t *testing.T) {
	env := NewEnvironment(nil, DefaultOptions())
	env.AddFilter("shout", func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return strings.ToUpper(s) + "!", nil
	})
	env.AddTest("shouty", func(fc *FilterContext, v any, _ []any) (bool, error) {
		s, _ := v.(string)
		return strings.HasSuffix(s, "!"), nil
	})

	res, err := env.RenderString(`{{ 'hi' | shout }} {{ 'hi!' is shouty }}`, "t.sls", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "HI! True" {
		t.Errorf("output = %q", res.Output)
	}
	if !containsName(env.FilterNames(), "shout") {
		t.Error("FilterNames does not list the added filter")
	}
	if !containsName(env.TestNames(), "shouty") {
		t.Error("TestNames does not list the added test")
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestKeepTrailingNewline(t *testing.T) {
	// Salt renders configuration files through this engine, so a trailing
	// newline is kept by default; a file that loses it can break the tool
	// that reads it.
	kept := renderWith(t, "line\n", nil, nil, nil)
	if kept != "line\n" {
		t.Errorf("default = %q, want the newline kept", kept)
	}
	stripped := renderWith(t, "line\n", nil, nil, func(o *Options) { o.KeepTrailingNewline = false })
	if stripped != "line" {
		t.Errorf("with KeepTrailingNewline off = %q", stripped)
	}
}

func TestTrimAndLstripBlocks(t *testing.T) {
	src := "{% if true %}\nkept\n{% endif %}\n"
	withTrim := renderWith(t, src, nil, nil, func(o *Options) { o.TrimBlocks = true })
	if strings.HasPrefix(withTrim, "\n") {
		t.Errorf("trim_blocks did not remove the newline after the tag: %q", withTrim)
	}

	src = "  {% if true %}kept{% endif %}"
	withLstrip := renderWith(t, src, nil, nil, func(o *Options) { o.LstripBlocks = true })
	if strings.HasPrefix(withLstrip, " ") {
		t.Errorf("lstrip_blocks did not strip the indentation: %q", withLstrip)
	}
}

func TestCustomDelimiters(t *testing.T) {
	// Some SLS files template a file that itself contains {{ and need the
	// delimiters moved.
	got := renderWith(t, "[[ x ]] and {{ literal }}", map[string]any{"x": "value"}, nil, func(o *Options) {
		o.Delimiters = Delimiters{
			VarStart: "[[", VarEnd: "]]",
			BlockStart: "[%", BlockEnd: "%]",
			CommentStart: "[#", CommentEnd: "#]",
		}
	})
	if got != "value and {{ literal }}" {
		t.Errorf("output = %q", got)
	}
}

func TestAutoescapeIsANoOp(t *testing.T) {
	// SLS output is not HTML, and escaping it would corrupt it. The tag
	// is parsed so a tree carrying it renders; the escaping is explicit.
	got := render(t, `{% autoescape true %}<b>{{ x }}</b>{% endautoescape %}`, map[string]any{"x": "<i>"})
	if got != "<b><i></b>" {
		t.Errorf("autoescape should not escape, got %q", got)
	}
}

func TestIncludeAcceptsACandidateList(t *testing.T) {
	loader := mapLoader{"second.jinja": "found the second"}
	got := renderWith(t, `{% include ['first.jinja', 'second.jinja'] %}`, nil, loader, nil)
	if got != "found the second" {
		t.Errorf("output = %q", got)
	}
}

func TestNamespaceAndLoopInteraction(t *testing.T) {
	// The idiom a tree uses to accumulate across a loop, which plain
	// `set` cannot do because a loop body is its own scope.
	src := `{%- set ns = namespace(found=false, names=[]) -%}
{%- for host in hosts %}
{%- if host.role == 'web' %}{% set ns.found = true %}{% endif %}
{%- endfor -%}
{{ ns.found }}`
	hosts := []any{
		value.MapOf("role", "db"),
		value.MapOf("role", "web"),
	}
	if got := render(t, src, map[string]any{"hosts": hosts}); got != "True" {
		t.Errorf("output = %q", got)
	}
}

func TestListMethods(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ [1,2,2,3].count(2) }}`, "2"},
		{`{{ ['a','b','c'].index('b') }}`, "1"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestMapMutationMethods(t *testing.T) {
	m := value.MapOf("a", int64(1))
	src := `{% do m.update({'b': 2}) %}{{ m.keys() | join(',') }}` +
		`{% do m.setdefault('c', 3) %}|{{ m.get('c') }}` +
		`{% set gone = m.pop('a') %}|{{ gone }}|{{ m.keys() | join(',') }}`
	got := render(t, src, map[string]any{"m": m})
	if got != "a,b|3|1|b,c" {
		t.Errorf("output = %q", got)
	}
}

func TestStringMethodsLongTail(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ 'a-b-c'.rsplit('-') | length }}`, "3"},
		{`{{ 'xxabxx'.strip('x') }}`, "ab"},
		{`{{ 'xxab'.lstrip('x') }}`, "ab"},
		{`{{ 'abxx'.rstrip('x') }}`, "ab"},
		{`{{ 'hello'.find('ll') }}`, "2"},
		{`{{ 'hello'.rfind('l') }}`, "3"},
		{`{{ 'hello'.count('l') }}`, "2"},
		{`{{ 'ABC'.swapcase() if false else 'abc'.upper() }}`, "ABC"},
		{`{{ 'a b  c'.split() | length }}`, "3"},
		{`{{ 'utf8'.encode() }}`, "utf8"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestFilterLongTail(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ 'hello world how are you' | wordwrap(11) | replace('\n', '|') }}`, "hello world|how are you"},
		{`{{ 'a b' | urlencode }}`, "a%20b"},
		{`{{ [1,2,3,4] | stdev | round(3) }}`, "1.291"},
		{`{{ {'a': 1} | xmlattr }}`, ` a="1"`},
		{`{{ 'long sentence here' | truncate(12, true, '~') }}`, "long senten~"},
		{`{{ 'abc' | center(7) }}`, "  abc  "},
		{`{{ none | length }}`, "0"},
		{`{{ {'a':1,'b':2} | length }}`, "2"},
		{`{{ 1755600000 | date_format('%H:%M') }}`, "10:40"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestAsTimeAcceptsSeveralForms(t *testing.T) {
	for _, src := range []string{
		`{{ '2026-08-19' | strftime('%Y') }}`,
		`{{ '2026-08-19T14:22:11Z' | strftime('%Y') }}`,
		`{{ 1755600000 | strftime('%Y') }}`,
	} {
		got := render(t, src, nil)
		if len(got) != 4 {
			t.Errorf("%s -> %q, want a four-digit year", src, got)
		}
	}
	// `now` and an empty value both mean the present.
	if got := render(t, `{{ 'now' | strftime('%Y') }}`, nil); got != time.Now().Format("2006") {
		t.Errorf("now = %q", got)
	}
	// Something that is not a timestamp is an error naming the value.
	err := renderErr(t, `{{ 'not a time' | strftime }}`, nil)
	mustContain(t, err.Error(), "not a time")
}

func TestCallSiteUnpacking(t *testing.T) {
	src := `{% macro m(a, b, c) %}{{ a }}{{ b }}{{ c }}{% endmacro %}` +
		`{{ m(*[1, 2], **{'c': 3}) }}`
	if got := render(t, src, nil); got != "123" {
		t.Errorf("output = %q", got)
	}
	// Unpacking something that is not a sequence or a mapping is an error
	// naming what it got.
	mustContain(t, renderErr(t, `{{ range(*1) }}`, nil).Error(), "must unpack a sequence")
	mustContain(t, renderErr(t, `{{ range(**1) }}`, nil).Error(), "must unpack a mapping")
}

func TestTokenAndTypeNamesInErrors(t *testing.T) {
	// The message an operator reads when a tag is malformed has to name
	// what was expected and what was found.
	cases := []struct{ src, want string }{
		{`{% for %}{% endfor %}`, "expected a name"},
		{`{% block %}{% endblock %}`, "expected a name"},
		{`{% import 'x' %}`, "expected as"},
		{`{{ 'a' is }}`, "expected a test name"},
		{`{{ x. }}`, "expected an attribute name"},
		{`{{ [1,2 }}`, "expected"},
		{`{{ {'a' 1} }}`, "expected"},
		{`{% set %}`, "expected a name"},
		{`{% with x %}{% endwith %}`, "expected"},
	}
	for _, c := range cases {
		mustContain(t, renderErr(t, c.src, nil).Error(), c.want)
	}
}

func TestDispatcherBothSpellings(t *testing.T) {
	d := &recordingDispatcher{}
	ctx := map[string]any{"salt": NewDispatch(d)}

	// SPEC section 10.2.7 requires both spellings to work.
	if got := render(t, `{{ salt['test.echo']('a') }}`, ctx); got != "test.echo(a)" {
		t.Errorf("bracket form = %q", got)
	}
	if got := render(t, `{{ salt.test.echo('b') }}`, ctx); got != "test.echo(b)" {
		t.Errorf("attribute form = %q", got)
	}
	// Calling the dispatcher itself is an error that says how to call it.
	mustContain(t, renderErr(t, `{{ salt() }}`, ctx).Error(), "salt['module.function']")
}

type recordingDispatcher struct{ calls []string }

func (d *recordingDispatcher) CallModule(name string, args []any, _ map[string]any) (any, error) {
	d.calls = append(d.calls, name)
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = renderValue(a)
	}
	return name + "(" + strings.Join(parts, ",") + ")", nil
}

func (d *recordingDispatcher) HasModule(name string) bool { return true }

func TestUndefinedRendersAndDescribes(t *testing.T) {
	u := Undefined{Name: "pillar.nginx", Hint: "mapping has no key nginx"}
	if u.String() != "" {
		t.Errorf("an undefined renders as the empty string, got %q", u.String())
	}
	if !strings.Contains(u.describe(), "nginx") {
		t.Errorf("describe = %q", u.describe())
	}
	if !IsUndefined(u) || IsUndefined("x") {
		t.Error("IsUndefined is wrong")
	}
}

func TestRenderValueAndRepr(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "None"},
		{true, "True"},
		{false, "False"},
		{int64(3), "3"},
		{3.5, "3.5"},
		{4.0, "4.0"},
		{[]byte("hi"), "hi"},
		{"plain", "plain"},
		{[]any{int64(1), "a"}, "[1, 'a']"},
		{value.MapOf("k", "v"), "{'k': 'v'}"},
	}
	for _, c := range cases {
		if got := renderValue(c.in); got != c.want {
			t.Errorf("renderValue(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
	// repr quotes a string, and picks a quote that does not collide.
	if got := reprValue(`it's`); got != `"it's"` {
		t.Errorf("repr = %s", got)
	}
}

func TestIterationOverEveryKind(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{% for c in 'abc' %}{{ c }}.{% endfor %}`, "a.b.c."},
		{`{% for k in m %}{{ k }}{% endfor %}`, "ab"},
		{`{% for i in [1,2] %}{{ i }}{% endfor %}`, "12"},
	}
	ctx := map[string]any{"m": value.MapOf("a", 1, "b", 2)}
	for _, c := range cases {
		if got := render(t, c.src, ctx); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
	// Iterating something that is not iterable names what it got.
	mustContain(t, renderErr(t, `{% for x in none %}{% endfor %}`, nil).Error(), "cannot iterate over none")
	mustContain(t, renderErr(t, `{% for x in 1 %}{% endfor %}`, nil).Error(), "cannot iterate over")
}

func TestSourceMapIsLineAccurate(t *testing.T) {
	// The property the YAML stage depends on: verbatim text maps line for
	// line, so an error deep in rendered output still names the source.
	src := "one\ntwo\n{% for i in range(5) %}\nloop {{ i }}\n{% endfor %}\nlast\n"
	env := NewEnvironment(nil, DefaultOptions())
	res, err := env.RenderString(src, "t.sls", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Line 1 and 2 of the output are line 1 and 2 of the source.
	for out, want := range map[int]int{1: 1, 2: 2} {
		pos, ok := res.MapLine(out)
		if !ok || pos.Line != want {
			t.Errorf("output line %d mapped to %v, want source line %d", out, pos, want)
		}
	}
	// The last output line comes from the last source line.
	lines := strings.Count(res.Output, "\n")
	pos, ok := res.MapLine(lines)
	if !ok || pos.Line < 5 {
		t.Errorf("the last output line mapped to %v, want line 5 or 6", pos)
	}
}

func TestErrorUnwrapCarriesTheCause(t *testing.T) {
	inner := os.ErrNotExist
	e := &Error{Pos: Pos{File: "a", Line: 1}, Msg: "wrapped", Cause: inner}
	if e.Unwrap() != inner {
		t.Error("Unwrap did not return the cause")
	}
	// A positionless error renders as its message alone.
	bare := &Error{Msg: "no position"}
	if bare.Error() != "no position" {
		t.Errorf("bare error = %q", bare.Error())
	}
}

func TestBlockSetInheritsAndSuperChains(t *testing.T) {
	loader := mapLoader{
		"base.jinja":   `[{% block body %}base{% endblock %}]`,
		"middle.jinja": `{% extends 'base.jinja' %}{% block body %}<{{ super() }}>{% endblock %}`,
	}
	got := renderWith(t,
		`{% extends 'middle.jinja' %}{% block body %}({{ super() }}){% endblock %}`,
		nil, loader, nil)
	if got != "[(<base>)]" {
		t.Errorf("a three-level super chain = %q", got)
	}
}

func TestChildTemplateDefinitionsRun(t *testing.T) {
	// A child commonly sets a variable or defines a macro that its blocks
	// use; its top-level output is discarded but its definitions are not.
	loader := mapLoader{"base.jinja": `{% block body %}{% endblock %}`}
	got := renderWith(t,
		`{% extends 'base.jinja' %}{% set who = 'ops' %}discarded{% block body %}hello {{ who }}{% endblock %}`,
		nil, loader, nil)
	if got != "hello ops" {
		t.Errorf("output = %q", got)
	}
}

func TestDeadlineIsEnforced(t *testing.T) {
	env := NewEnvironment(nil, func() Options {
		o := DefaultOptions()
		o.Timeout = time.Nanosecond
		o.MaxIterations = 1 << 30
		return o
	}())
	_, err := env.RenderString(`{% for i in range(200000) %}{{ i }}{% endfor %}`, "t.sls", nil)
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected a deadline error, got %v", err)
	}
}

func TestNestingDepthIsBounded(t *testing.T) {
	src := strings.Repeat("{% if true %}", 200) + "x" + strings.Repeat("{% endif %}", 200)
	env := NewEnvironment(nil, DefaultOptions())
	_, err := env.RenderString(src, "t.sls", nil)
	if err == nil || !strings.Contains(err.Error(), "deeper than") {
		t.Fatalf("expected a nesting error, got %v", err)
	}
}

func TestValueCoercionLongTail(t *testing.T) {
	// The comparison and coercion helpers carry the arithmetic every
	// expression goes through, so their edge cases are worth naming.
	cases := []struct{ src, want string }{
		// Booleans coerce to numbers, as in Python.
		{`{{ true + 1 }}`, "2"},
		{`{{ true == 1 }}`, "True"},
		{`{{ [1] == [1] }}`, "True"},
		{`{{ [1] == [2] }}`, "False"},
		{`{{ {'a':1} == {'a':1} }}`, "True"},
		{`{{ {'a':1} == {'a':2} }}`, "False"},
		{`{{ none == none }}`, "True"},
		{`{{ 'a' == 1 }}`, "False"},
		// Membership across each container kind.
		{`{{ 'a' in {'a': 1} }}`, "True"},
		{`{{ 1 in [1,2] }}`, "True"},
		{`{{ 'z' in 'abc' }}`, "False"},
		// Negative and out-of-range indexing.
		{`{{ 'abc'[-1] }}`, "c"},
		{`{{ [1,2,3][-2] }}`, "2"},
		{`{{ [1,2][9] | default('gone') }}`, "gone"},
		{`{{ 'ab'[9] | default('gone') }}`, "gone"},
		// int() across its accepted inputs.
		{`{{ true | int }}`, "1"},
		{`{{ 2.9 | int }}`, "2"},
		{`{{ ' 12 ' | int }}`, "12"},
		{`{{ '1.9' | int }}`, "1"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestComparisonErrorsNameBothTypes(t *testing.T) {
	err := renderErr(t, `{{ 'a' < 1 }}`, nil)
	mustContain(t, err.Error(), "cannot compare")
	err = renderErr(t, `{{ [1] < [2] }}`, nil)
	mustContain(t, err.Error(), "cannot compare")
	err = renderErr(t, `{{ -'a' }}`, nil)
	mustContain(t, err.Error(), "cannot negate")
	err = renderErr(t, `{{ {'a':1} * 2 }}`, nil)
	mustContain(t, err.Error(), "cannot apply")
}

func TestStringFormatEdgeCases(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ '{{literal}}'.format() }}`, "{literal}"},
		{`{{ '{0}-{0}'.format('x') }}`, "x-x"},
		{`{{ '{a}{b}'.format(a=1, b=2) }}`, "12"},
		{`{{ '{:>5}'.format('x') }}`, "x"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
	mustContain(t, renderErr(t, `{{ '{'.format() }}`, nil).Error(), "unmatched")
	mustContain(t, renderErr(t, `{{ '}'.format() }}`, nil).Error(), "unmatched")
	mustContain(t, renderErr(t, `{{ '{}'.format() }}`, nil).Error(), "not enough positional")
	mustContain(t, renderErr(t, `{{ '{z}'.format() }}`, nil).Error(), "keyword argument")
}

func TestLexerNumberAndStringForms(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ 1_000 }}`, "1000"},
		{`{{ 1.5e2 }}`, "150.0"},
		{`{{ 1.5E+2 }}`, "150.0"},
		{`{{ 1e-2 }}`, "0.01"},
		{`{{ "a" "b" }}`, "ab"},
		{`{{ "tab\there" }}`, "tab\there"},
		{`{{ "\x41B" }}`, "AB"},
		{`{{ "\q" }}`, `\q`},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
	mustContain(t, renderErr(t, `{{ "unterminated }}`, nil).Error(), "unterminated")
	mustContain(t, renderErr(t, `{{ 1 @ 2 }}`, nil).Error(), "unexpected character")
	mustContain(t, renderErr(t, `{# unterminated`, nil).Error(), "unterminated comment")
	mustContain(t, renderErr(t, `{% raw %}no end`, nil).Error(), "unterminated raw")
}

func TestSerializationFilterErrors(t *testing.T) {
	mustContain(t, renderErr(t, `{{ 'notjson' | json_decode_dict }}`, nil).Error(), "decoding JSON")
	mustContain(t, renderErr(t, `{{ '[1]' | json_decode_dict }}`, nil).Error(), "expected a JSON object")
	mustContain(t, renderErr(t, `{{ '{}' | json_decode_list }}`, nil).Error(), "expected a JSON array")
	mustContain(t, renderErr(t, `{{ 'maybe' | to_bool }}`, nil).Error(), "is not a boolean")
	mustContain(t, renderErr(t, `{{ 'abc' | to_num }}`, nil).Error(), "is not a number")
	mustContain(t, renderErr(t, `{{ 'a: [' | load_yaml }}`, nil).Error(), "load_yaml")
	mustContain(t, renderErr(t, `{{ '!!' | base64_decode }}`, nil).Error(), "base64_decode")

	// The forms that do work.
	cases := []struct{ src, want string }{
		{`{{ none | to_bool }}`, "False"},
		{`{{ 1 | to_bool }}`, "True"},
		{`{{ '  7 ' | to_num }}`, "7"},
		{`{{ '1.5' | to_num }}`, "1.5"},
		{`{{ '616263' | hex_decode }}`, "abc"},
		{`{{ {'a': 1} | dict_to_sls_yaml_params }}`, "- a: 1"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestGlobalsAndTheirLimits(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ range(3) }}`, "[0, 1, 2]"},
		{`{{ range(1, 4) }}`, "[1, 2, 3]"},
		{`{{ range(0, 6, 2) }}`, "[0, 2, 4]"},
		{`{{ range(3, 0, -1) }}`, "[3, 2, 1]"},
		{`{{ dict(a=1).keys() | join(',') }}`, "a"},
		{`{{ dict({'b': 2}).keys() | join(',') }}`, "b"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
	mustContain(t, renderErr(t, `{{ range(1, 2, 0) }}`, nil).Error(), "step cannot be zero")
	mustContain(t, renderErr(t, `{{ range() }}`, nil).Error(), "one to three arguments")
	// range() materialises, so it is bounded by the same limit a loop is.
	mustContain(t, renderErr(t, `{{ range(100000000) }}`, nil).Error(), "past the limit")
	mustContain(t, renderErr(t, `{{ lipsum() }}`, nil).Error(), "not supported")
}
