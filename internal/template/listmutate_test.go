package template

import (
	"strings"
	"testing"
)

// The shape every one of the estate's six files uses: a list built at
// one level and appended to from inside a loop or a macro.
//
// This is the case `append` exists for. `{% set %}` inside a loop body
// does not survive the loop, so a tree that needs to accumulate reaches
// for a method that mutates the object instead -- and an implementation
// that rebinds in the innermost scope reproduces the bug rather than
// the fix. `shared/salt/files/minion.d/defaults.conf` appends from // lexicon:allow — the estate's own path
// inside a macro to a list declared at the top of the template.
func TestAppendSurvivesTheScopeItHappensIn(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"a for body",
			`{% set l = [] %}{% for i in [1,2,3] %}{% do l.append(i) %}{% endfor %}{{ l }}`,
			"[1, 2, 3]"},
		{"nested for bodies",
			`{% set l = [] %}{% for a in ['x','y'] %}{% for b in [1,2] %}` +
				`{% do l.append(a ~ b) %}{% endfor %}{% endfor %}{{ l }}`,
			"['x1', 'x2', 'y1', 'y2']"},
		{"a macro, which is the estate's defaults.conf",
			`{% set keys = [] %}{% macro g(n) %}{% do keys.append(n) %}{% endmacro %}` +
				`{{ g('a') }}{{ g('b') }}{{ keys }}`,
			"['a', 'b']"},
		{"an if body",
			`{% set l = [] %}{% if true %}{% do l.append(1) %}{% endif %}{{ l }}`,
			"[1]"},
	} {
		if got := render(t, tc.src, nil); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A local binding is the one that is updated, not an outer one of the
// same name. Rebinding the wrong scope is the mirror image of the bug
// above and just as wrong.
func TestAppendUpdatesTheInnermostBinding(t *testing.T) {
	src := `{% set l = ['outer'] %}` +
		`{% for i in [1] %}{% set l = [] %}{% do l.append('inner') %}{{ l }}{% endfor %}` +
		`{{ l }}`
	if got := render(t, src, nil); got != "['inner']['outer']" {
		t.Errorf("got %q", got)
	}
}

// The rest of Python's list API, which a tree reaches for far less
// often but reaches for.
func TestTheMutatingListMethods(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"extend", `{% set l = [1] %}{% do l.extend([2,3]) %}{{ l }}`, "[1, 2, 3]"},
		{"extend from a mapping's keys", `{% set l = [] %}{% do l.extend({'a':1}) %}{{ l }}`, "['a']"},
		{"insert", `{% set l = [1,3] %}{% do l.insert(1, 2) %}{{ l }}`, "[1, 2, 3]"},
		{"insert past the end appends", `{% set l = [1] %}{% do l.insert(99, 2) %}{{ l }}`, "[1, 2]"},
		{"insert negative", `{% set l = [1,3] %}{% do l.insert(-1, 2) %}{{ l }}`, "[1, 2, 3]"},
		{"remove", `{% set l = [1,2,3] %}{% do l.remove(2) %}{{ l }}`, "[1, 3]"},
		{"remove takes the first only", `{% set l = [1,2,2] %}{% do l.remove(2) %}{{ l }}`, "[1, 2]"},
		{"pop returns and removes", `{% set l = [1,2] %}{{ l.pop() }}{{ l }}`, "2[1]"},
		{"pop by index", `{% set l = [1,2,3] %}{{ l.pop(0) }}{{ l }}`, "1[2, 3]"},
		{"pop negative", `{% set l = [1,2,3] %}{{ l.pop(-2) }}{{ l }}`, "2[1, 3]"},
		{"clear", `{% set l = [1,2] %}{% do l.clear() %}{{ l }}`, "[]"},
		{"reverse", `{% set l = [1,2,3] %}{% do l.reverse() %}{{ l }}`, "[3, 2, 1]"},
		{"sort", `{% set l = [3,1,2] %}{% do l.sort() %}{{ l }}`, "[1, 2, 3]"},
		{"sort strings", `{% set l = ['b','a'] %}{% do l.sort() %}{{ l }}`, "['a', 'b']"},
		{"sort reverse", `{% set l = [1,3,2] %}{% do l.sort(reverse=true) %}{{ l }}`, "[3, 2, 1]"},
	} {
		if got := render(t, tc.src, nil); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A list reached through a mapping or another list is mutated where it
// lives, because the container is shared.
func TestAppendThroughAContainer(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"a mapping subscript", `{% set m = {'k': []} %}{% do m['k'].append(1) %}{{ m }}`, "{'k': [1]}"},
		{"a mapping attribute", `{% set m = {'k': []} %}{% do m.k.append(1) %}{{ m }}`, "{'k': [1]}"},
		{"a list element", `{% set l = [[]] %}{% do l[0].append(1) %}{{ l }}`, "[[1]]"},
		{"a namespace", `{% set ns = namespace(l=[]) %}{% do ns.l.append(1) %}{{ ns.l }}`, "[1]"},
	} {
		if got := render(t, tc.src, nil); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// These names are not only list methods. A mapping has its own `pop`
// and `update`, a string its own `split` and `count`, and resolving a
// mutating list method too eagerly takes those over.
func TestMethodsOnOtherTypesAreUntouched(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"mapping pop", `{% set m = {'a': 1, 'b': 2} %}{{ m.pop('a') }}{{ m }}`, "1{'b': 2}"},
		{"mapping update", `{% set m = {} %}{% do m.update({'a': 1}) %}{{ m }}`, "{'a': 1}"},
		{"string split", `{{ 'a.b'.split('.') }}`, "['a', 'b']"},
		{"string count", `{{ 'aba'.count('a') }}`, "2"},
		{"list count is not mutating", `{% set l = [1,2,2] %}{{ l.count(2) }}{{ l }}`, "2[1, 2, 2]"},
		{"list index is not mutating", `{% set l = [1,2] %}{{ l.index(2) }}{{ l }}`, "1[1, 2]"},
	} {
		if got := render(t, tc.src, nil); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A bad call says what is wrong rather than rendering something.
func TestMutatingListMethodErrors(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`{% set l = [] %}{% do l.pop() %}`, "empty"},
		{`{% set l = [1] %}{% do l.remove(2) %}`, "not in the list"},
		{`{% set l = [1] %}{% do l.pop(5) %}`, "out of range"},
		{`{% set l = [] %}{% do l.append() %}`, "one argument"},
		{`{% set l = [] %}{% do l.extend(5) %}`, "sequence"},
	} {
		err := renderErr(t, tc.src, nil)
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error is %q, want it to mention %q", tc.src, err, tc.want)
		}
	}
}

// Mutating a value that is not a place is allowed and goes nowhere,
// which is what Python does with a temporary too. What must not happen
// is a crash or an "undefined" that stops the render.
func TestMutatingATemporaryIsHarmless(t *testing.T) {
	if got := render(t, `{% do [1,2].append(3) %}ok`, nil); got != "ok" {
		t.Errorf("got %q", got)
	}
}

// A nil key reached sortAny as the zero value of its parameter and was
// called on every comparison. A panic inside a render is a node crash
// rather than an error an operator can read.
func TestSortAnyToleratesANilKey(t *testing.T) {
	items := []any{int64(3), int64(1), int64(2)}
	sortAny(items, false, true, nil)
	if items[0] != int64(1) || items[2] != int64(3) {
		t.Errorf("sorted to %v", items)
	}
}

// Salt's `json` filter is not Jinja's `tojson`. `format_json` in
// `salt/utils/jinja.py` is `json.dumps(value, sort_keys=True,
// indent=None).strip()`, so a mapping comes out in key order; `tojson`
// writes it in the order it was built. A tree rendering a config file
// through `|json` produced sorted output under Salt, and insertion
// order here would make the file differ on the first run after a
// migration.
//
// Every expectation below was taken from the Salt on the reference
// host rather than read off its source:
//
//	salt.utils.json.dumps(value, sort_keys=True, indent=None).strip()
func TestSaltJSONFilterMatchesSalt(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`{{ {'b': 1, 'a': 2}|json }}`, `{"a": 2, "b": 1}`},
		{`{{ {'b': {'d': 1, 'c': 2}}|json }}`, `{"b": {"c": 2, "d": 1}}`},
		// A list is a document's order, not a set of keys, so sort_keys
		// leaves it alone.
		{`{{ [3,1,2]|json }}`, `[3, 1, 2]`},
		{`{{ 'x'|json }}`, `"x"`},
		{`{{ {'a': 1}|json(indent=2) }}`, "{\n  \"a\": 1\n}"},
		{`{{ {'b': 1, 'a': 2}|json(sort_keys=false) }}`, `{"b": 1, "a": 2}`},
	} {
		if got := render(t, tc.src, nil); got != tc.want {
			t.Errorf("%s\n got %q\nwant %q", tc.src, got, tc.want)
		}
	}
}

// And tojson keeps Jinja's behaviour, which is the reason both exist.
func TestTojsonDoesNotSort(t *testing.T) {
	if got := render(t, `{{ {'b': 1, 'a': 2}|tojson }}`, nil); got != `{"b": 1, "a": 2}` {
		t.Errorf("got %q", got)
	}
}
