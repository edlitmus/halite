package migrate

import (
	"strings"
	"testing"
)

// undefinedSubjects returns the names an audit of one file reported.
func undefinedSubjects(t *testing.T, sls string) []string {
	t.Helper()
	root := writeSLSTree(t, map[string]string{"base/t.sls": sls})
	var out []string
	for _, f := range auditSLSTree(t, root).Findings {
		if f.Category == CatUndefined {
			out = append(out, f.Subject)
		}
	}
	return out
}

// SPEC 28.5 asks the report for every name that would fail under strict
// undefined, and `CatUndefined` was declared and never emitted — so no
// report could tell an estate what halite's default costs it, which is
// what SPEC 33 question 4 is waiting on.
//
// Decided statically. Rendering would need this estate's pillar and
// grains, and would report every pillar value as undefined.
func TestAnUndefinedNameIsReported(t *testing.T) {
	got := undefinedSubjects(t, `{{ missing }}
thing:
  pkg.installed:
    - name: {{ also_missing }}
`)
	want := map[string]bool{"missing": true, "also_missing": true}
	if len(got) != 2 {
		t.Fatalf("got %v, want the two undefined names", got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("%q was reported and is not one of the undefined names", n)
		}
	}
}

// It blocks, because strict is the default this build renders with. The
// file does not compile as written; it is not a prediction about a
// setting somebody might turn on.
func TestAnUndefinedNameBlocks(t *testing.T) {
	root := writeSLSTree(t, map[string]string{"base/t.sls": "{{ missing }}\n"})
	rep := auditSLSTree(t, root)
	if rep.Count().Blocking == 0 {
		t.Error("an undefined name does not block, and the file it is in does not render")
	}
	for _, f := range rep.Findings {
		if f.Category != CatUndefined {
			continue
		}
		if !strings.Contains(f.Action, "permissive") {
			t.Error("the finding does not name the transition that makes it non-fatal")
		}
	}
}

// Everything that binds a name has to be understood, or the audit
// reports a tree that is fine. Each line here is a construct that binds,
// and none of them may produce a finding.
func TestNothingBoundIsReportedUndefined(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"set", `{% set x = 1 %}{{ x }}`},
		{"block set", `{% set x %}v{% endset %}{{ x }}`},
		{"for target", `{% for x in [1] %}{{ x }}{% endfor %}`},
		{"for tuple target", `{% for k, v in [] %}{{ k }}{{ v }}{% endfor %}`},
		{"loop", `{% for x in [1] %}{{ loop.index }}{% endfor %}`},
		{"macro name and params", `{% macro m(a, b=1) %}{{ a }}{{ b }}{% endmacro %}{{ m(1) }}`},
		{"with", `{% with y = 1 %}{{ y }}{% endwith %}`},
		{"import alias", `{% import "o.sls" as o %}{{ o.thing }}`},
		{"from import", `{% from "o.sls" import a %}{{ a }}`},
		{"from import alias", `{% from "o.sls" import a as b %}{{ b }}`},
		{"caller", `{% macro m() %}{{ caller() }}{% endmacro %}{% call m() %}x{% endcall %}`},

		// The render context, which a template is given without
		// defining. Read from the renderer rather than listed here, so
		// this is a check that the wiring holds.
		{"pillar", `{{ pillar.get('a') }}`},
		{"grains", `{{ grains['os'] }}`},
		{"salt dispatch", `{{ salt['test.ping']() }}`},
		{"path helpers", `{{ slspath }}{{ tplfile }}{{ sls }}{{ saltenv }}`},
		{"dunders", `{{ __env__ }}{{ __pillar__ }}`},
		{"jinja globals", `{{ range(3) }}{{ dict(a=1) }}{{ namespace(x=1) }}`},

		// A reactor SLS is a `.sls` like any other and nothing says
		// which it is, so its two extra names count as defined.
		{"reaction data", `{{ data['id'] }}{{ tag }}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := undefinedSubjects(t, tc.src+"\n"); len(got) != 0 {
				t.Errorf("%s reported %v, and every name in it is bound", tc.name, got)
			}
		})
	}
}

// A name is reported once per expression that reads it, not once per
// occurrence, so a loop over a missing variable does not fill the report
// with the same line.
func TestAnUndefinedNameIsNotReportedTwiceForOneExpression(t *testing.T) {
	got := undefinedSubjects(t, "{{ missing + missing }}\n")
	if len(got) != 1 {
		t.Errorf("got %v, want one finding for one expression", got)
	}
}

// The value of a set is read before the name it defines is in scope, so
// `{% set x = x %}` reads an outer x and reports it when there is none.
func TestASetReadsItsOwnValueBeforeDefining(t *testing.T) {
	if got := undefinedSubjects(t, "{% set x = x %}{{ x }}\n"); len(got) != 1 {
		t.Errorf("got %v, want the read of the outer x reported", got)
	}
}
