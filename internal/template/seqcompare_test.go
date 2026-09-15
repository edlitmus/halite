package template

import "testing"

// Two sequences compare element by element, and the shorter is the
// smaller where one runs out. That is Python's rule and so Jinja's, and
// an estate's tree gates on a version with it:
//
//	{% if grains['saltversioninfo'] >= [2016, 3] %}
//
// Before this, that comparison was "cannot compare sequence with
// sequence" -- which a tree meets only once the grain exists, so filling
// in the grain is what exposed it.
func TestSequenceComparison(t *testing.T) {
	seq := func(v ...any) []any { return v }

	for _, tc := range []struct {
		name string
		a, b []any
		want int
	}{
		{"the version gate an estate's tree writes", seq(int64(3007), int64(1)), seq(int64(2016), int64(3)), 1},
		{"the first differing element decides", seq(int64(1), int64(9)), seq(int64(2), int64(0)), -1},
		{"and it decides even when later elements disagree", seq(int64(3), int64(0)), seq(int64(1), int64(99)), 1},
		{"equal sequences", seq(int64(2), int64(2)), seq(int64(2), int64(2)), 0},
		{"a prefix is smaller than what extends it", seq(int64(3007)), seq(int64(3007), int64(1)), -1},
		{"and longer is larger", seq(int64(3007), int64(1)), seq(int64(3007)), 1},
		{"empty against empty", seq(), seq(), 0},
		{"empty is smaller than anything", seq(), seq(int64(0)), -1},
		{"strings compare as strings", seq("a", "b"), seq("a", "c"), -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compare(tc.a, tc.b)
			if err != nil {
				t.Fatalf("compare(%v, %v): %v", tc.a, tc.b, err)
			}
			if got != tc.want {
				t.Errorf("compare(%v, %v) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// A sequence and a scalar have no order between them, and neither do
// elements of different kinds. Both are errors rather than an answer,
// because any answer would be this build inventing one.
func TestSequenceComparisonRefusesWhatHasNoOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b any
	}{
		{"a sequence against a number", []any{int64(1)}, int64(1)},
		{"a sequence against a string", []any{int64(1)}, "1"},
		{"elements of different kinds", []any{"a"}, []any{int64(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := compare(tc.a, tc.b); err == nil {
				t.Errorf("compare(%v, %v) gave an answer where there is no order", tc.a, tc.b)
			}
		})
	}
}
