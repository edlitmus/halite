package template

import (
	"strings"
	"testing"
)

// A template is the one part of a state tree that is executed rather than
// read, and SPEC section 31 names its lexer and parser as fuzz targets. A
// panic here takes the node's whole run down, and the budgets that bound
// output size, iteration count, and recursion depth are exactly the kind
// of thing a fuzzer finds a way around.
func FuzzRender(f *testing.F) {
	seeds := []string{
		"plain text",
		"{{ name }}",
		"{{ name | upper | trim }}",
		"{% if x %}a{% else %}b{% endif %}",
		"{% for i in items %}{{ i }}{% endfor %}",
		"{% for i in items recursive %}{{ loop.depth }}{% endfor %}",
		"{% set x = 1 %}{{ x }}",
		"{% macro m(a, b=2) %}{{ a }}{{ b }}{{ caller() }}{% endmacro %}{{ m(1) }}",
		"{% call m(1) %}body{% endcall %}",
		"{% raw %}{{ not a variable }}{% endraw %}",
		"{# a comment #}",
		"{{ [1, 2, 3][1:2] }}",
		"{{ {'a': 1}['a'] }}",
		"{{ 1 if true else 2 }}",
		"{{ 'x' ~ 'y' }}",
		"{{ items | map('upper') | join(',') }}",
		"{{ 1 + 2 * 3 ** 4 // 5 % 6 }}",
		"{% filter upper %}text{% endfilter %}",
		"{% block b %}{{ super() }}{% endblock %}",
		"{{- whitespace -}}",
		"{% for i in range(9999999) %}x{% endfor %}",
		"{{ 'a' * 999999999 }}",
		"{% set x = x %}{{ x }}",
		"{{",
		"{%",
		"{{ }}",
		"{% endfor %}",
		strings.Repeat("{% if x %}", 100),
		strings.Repeat("(", 200) + "1" + strings.Repeat(")", 200),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		env := NewEnvironment(nil, fuzzOptions())
		ctx := map[string]any{
			"name":  "value",
			"x":     true,
			"items": []any{"a", "b", "c"},
		}
		// Rendering either produces output or reports an error. A panic is
		// a defect, and so is a hang: the budgets in DefaultOptions have
		// to catch the runaway loops among the seeds above.
		res, err := env.RenderString(src, "fuzz.sls", ctx)
		if err != nil {
			return
		}
		if int64(len(res.Output)) > env.Opts.MaxOutput {
			t.Errorf("output of %d bytes exceeds the %d-byte budget", len(res.Output), env.Opts.MaxOutput)
		}
	})
}

// The strict-undefined mode of SPEC section 10.2.6 is the default and is
// what most templates in a real tree will hit first, so it gets its own
// corpus rather than sharing one with the permissive path.
func FuzzRenderStrictUndefined(f *testing.F) {
	f.Add("{{ missing }}")
	f.Add("{{ missing.attribute }}")
	f.Add("{{ missing['key'] }}")
	f.Add("{{ missing | default('d') }}")
	f.Add("{% if missing is defined %}a{% endif %}")
	f.Fuzz(func(t *testing.T, src string) {
		opts := fuzzOptions()
		opts.Undefined = Strict
		env := NewEnvironment(nil, opts)
		_, _ = env.RenderString(src, "fuzz.sls", map[string]any{})
	})
}

// fuzzOptions keeps the budget semantics but shrinks the numbers, so a
// runaway loop is caught in milliseconds rather than seconds. The property
// under test is that the budget stops it at all, not where.
func fuzzOptions() Options {
	o := DefaultOptions()
	o.MaxOutput = 1 << 16
	o.MaxIterations = 10000
	return o
}
