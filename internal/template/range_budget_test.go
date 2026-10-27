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
