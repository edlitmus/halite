package template

import (
	"strings"
	"testing"
)

// `{% break %}` and `{% continue %}` are Jinja's loopcontrols extension,
// which Salt enables.
//
// This build had `do` and `with` — Salt's other two — and not this one,
// so a tree that loops could not be rendered at all. It was the single
// hard parse failure across 193 files of a real estate, and the
// vendored Jinja corpus does not cover it, so only a real tree found it.
//
// Every expectation here was taken from Jinja 3 with
// `jinja2.ext.loopcontrols` loaded, which is the environment Salt builds.
func TestLoopControls(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{
			"break stops the loop",
			`{% for i in [1,2,3,4,5] %}{% if i == 3 %}{% break %}{% endif %}{{ i }}{% endfor %}`,
			"12",
		},
		{
			"continue skips one iteration",
			`{% for i in [1,2,3,4,5] %}{% if i % 2 == 0 %}{% continue %}{% endif %}{{ i }}{% endfor %}`,
			"135",
		},
		{
			"break leaves only the innermost loop",
			`{% for a in [1,2] %}{% for b in [1,2,3] %}{% if b == 2 %}{% break %}{% endif %}{{ a }}{{ b }} {% endfor %}{% endfor %}`,
			"11 21 ",
		},
		{
			"break on the first item produces nothing",
			`{% for i in [1,2,3] %}{% break %}{{ i }}{% endfor %}`,
			"",
		},
		{
			"continue on every item produces nothing",
			`{% for i in [1,2,3] %}{% continue %}{{ i }}{% endfor %}`,
			"",
		},
		// The else branch is where a reimplementation is most likely to
		// differ, and where mine did. Jinja runs it when no iteration
		// reached the end of the body — so breaking on the first item
		// runs it and breaking on the second does not. Every line here
		// was measured against Jinja with loopcontrols loaded, after I
		// asserted the opposite from reasoning and was wrong.
		{
			"breaking on the first item runs the else",
			`{% for i in [1,2] %}{% break %}x{% else %}none{% endfor %}`,
			"none",
		},
		{
			"breaking after one full pass does not run the else",
			`{% for i in [1,2,3] %}{% if i == 2 %}{% break %}{% endif %}x{% else %}none{% endfor %}`,
			"x",
		},
		{
			"continuing out of every pass runs the else",
			`{% for i in [1,2] %}{% continue %}x{% else %}none{% endfor %}`,
			"none",
		},
		{
			"an empty iterable still runs the else",
			`{% for i in [] %}x{% else %}none{% endfor %}`,
			"none",
		},
		{
			"a loop that completes does not run the else",
			`{% for i in [1,2] %}x{% else %}none{% endfor %}`,
			"xx",
		},
		{
			"break inside a filter block still reaches the loop",
			`{% for i in [1,2,3] %}{% filter upper %}{% if i == 2 %}{% break %}{% endif %}x{% endfilter %}{% endfor %}`,
			"X",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(t, tc.src, nil); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Outside a loop the tag is refused while the template is read, which is
// where Jinja refuses it — not when the branch happens to run.
func TestLoopControlOutsideALoopIsRefused(t *testing.T) {
	for _, src := range []string{
		`{% break %}`,
		`{% continue %}`,
		`{% if true %}{% break %}{% endif %}`,
		// A macro is its own lexical scope: Jinja refuses a break in one
		// even when the call site is inside a loop.
		`{% macro m() %}{% break %}{% endmacro %}{% for i in [1] %}{{ m() }}{% endfor %}`,
	} {
		err := renderErr(t, src, nil)
		if err == nil {
			t.Errorf("%s: rendered without error", src)
			continue
		}
		if !strings.Contains(err.Error(), "only meaningful inside a for loop") {
			t.Errorf("%s: unhelpful error: %v", src, err)
		}
	}
}

// A break must not escape the template as an error a caller sees. The
// parser is what guarantees it, and this is the assertion that says so
// from the outside.
func TestABreakNeverReachesTheCaller(t *testing.T) {
	out := render(t, `{% for i in [1,2,3] %}{% if i == 2 %}{% break %}{% endif %}{{ i }}{% endfor %}done`, nil)
	if out != "1done" {
		t.Errorf("got %q, want %q", out, "1done")
	}
}
