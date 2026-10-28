package template

import (
	"strings"
	"testing"
)

// One range() call was bounded by the iteration limit; a template could
// make as many calls as it had variables. Thirty calls of nine million
// items allocated 6.2 GB before this, each of them allowed.
func TestRangeListsAreChargedToTheRenderNotOnlyToTheCall(t *testing.T) {
	e := NewEnvironment(nil, DefaultOptions())
	src := "{% set a = range(6000000) %}{% set b = range(6000000) %}done"
	if _, err := e.RenderString(src, "t", nil); err == nil || !strings.Contains(err.Error(), "in all") {
		t.Errorf("two 6M lists in one render were allowed (limit 10M): %v", err)
	}
	// The ordinary uses are untouched.
	res, err := e.RenderString("{% for i in range(3) %}{{ i }}{% endfor %}{{ range(5)|length }}", "t", nil)
	if err != nil || res.Output != "0125" {
		t.Errorf("ordinary range use: %q, %v", res.Output, err)
	}
}

// permutations and combinations were bounded by the items one call holds,
// as range() was: each of three calls of 4.2 million items was allowed,
// 12.7 million in all against a limit of ten million.
func TestCombinatoricListsAreChargedToTheRender(t *testing.T) {
	e := NewEnvironment(nil, DefaultOptions())
	for _, filter := range []string{"permutations", "combinations"} {
		// permutations of ten items, seven at a time, is 604,800 results of
		// seven items; combinations of 22, eleven at a time, is 705,432
		// results of eleven, 7.8 million items, so two are past the limit.
		src := "{% set s = range(10)|list %}{% set a = s|permutations(7) %}{% set b = s|permutations(7) %}{% set c = s|permutations(7) %}done"
		if filter == "combinations" {
			src = "{% set s = range(22)|list %}{% set a = s|combinations(11) %}{% set b = s|combinations(11) %}done"
		}
		_, err := e.RenderString(src, "t", nil)
		if err == nil || !strings.Contains(err.Error(), "in all") {
			t.Errorf("%s: lists past the render's limit were allowed: %v", filter, err)
		}
	}
	res, err := e.RenderString("{{ [1, 2, 3]|permutations(2)|list|length }} {{ [1, 2, 3]|combinations(2)|list|length }}", "t", nil)
	if err != nil || res.Output != "6 3" {
		t.Errorf("ordinary use: %q, %v", res.Output, err)
	}
}

// List repetition had the same per-call bound: each `[0] * 6000000` was
// allowed, however many a render made.
func TestListRepetitionIsChargedToTheRender(t *testing.T) {
	e := NewEnvironment(nil, DefaultOptions())
	src := "{% set a = [0] * 6000000 %}{% set b = [0] * 6000000 %}done"
	if _, err := e.RenderString(src, "t", nil); err == nil || !strings.Contains(err.Error(), "in all") {
		t.Errorf("two 6M lists in one render were allowed (limit 10M): %v", err)
	}
	res, err := e.RenderString("{{ ([1, 2] * 3)|length }}", "t", nil)
	if err != nil || res.Output != "6" {
		t.Errorf("ordinary use: %q, %v", res.Output, err)
	}
}
