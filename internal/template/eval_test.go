package template

import "testing"

// TestDjangoNumericAttribute. `list.0` is Django's spelling of
// `list[0]`, which Jinja accepts because its getattr falls through to a
// subscript, and which Salt trees write. Two things had to give: the
// lexer reads `.0.0` as one float, because in every other position that
// is what it is; and a sequence has no key "0", so the fall-through had
// to reach the sequence subscript rather than the mapping lookup.
func TestDjangoNumericAttribute(t *testing.T) {
	for src, want := range map[string]string{
		"{{ [1, 2, 3].0 }}":    "1",
		"{{ [1, 2, 3].2 }}":    "3",
		"{{ [[1]].0.0 }}":      "1",
		"{{ {'a': [9]}.a.0 }}": "9",
		"{{ 'abc'.1 }}":        "b",
		// A float is still a float everywhere else.
		"{{ 1.5 }}":     "1.5",
		"{{ 1.5 + 1 }}": "2.5",
	} {
		out, err := NewEnvironment(nil, DefaultOptions()).RenderString(src, "t", nil)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if out.Output != want {
			t.Errorf("%s = %q, want %q", src, out.Output, want)
		}
	}

	// An index past the end is undefined rather than a crash.
	if _, err := NewEnvironment(nil, DefaultOptions()).RenderString("{{ [1].5 }}", "t", nil); err == nil {
		t.Error("an index past the end should be undefined under strict")
	}
}

// TestWithArgumentsAreEvaluatedOutside. Jinja evaluates every value of a
// `with` in the enclosing scope before binding any of the names, so
// `{% with x=1, y=x %}` gives y the outer x. Binding as it went made
// each value see the ones before it, which reads plausibly and is not
// what Jinja does.
func TestWithArgumentsAreEvaluatedOutside(t *testing.T) {
	out, err := NewEnvironment(nil, DefaultOptions()).RenderString(
		"{%- with a=1, b=2, c=b, d=e, e=5 -%}{{ a }}|{{ b }}|{{ c }}|{{ d }}|{{ e }}{%- endwith -%}",
		"t", map[string]any{"b": int64(3), "e": int64(4)})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != "1|2|3|4|5" {
		t.Errorf("got %q, want %q", out.Output, "1|2|3|4|5")
	}

	out, err = NewEnvironment(nil, DefaultOptions()).RenderString(
		"{%- set x = 9 -%}{%- with x=1, y=x -%}{{ x }}{{ y }}{%- endwith -%}", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != "19" {
		t.Errorf("got %q, want %q; y should see the outer x", out.Output, "19")
	}
}
