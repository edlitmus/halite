package hub

import (
	"reflect"
	"testing"
)

// `tolerate_failures` globs are Salt's, so `[!x]` negates.
//
// `fail_minions: ['web[!1]*']` in a Salt orchestration forgives every web node
// except web1. Matched with path.Match it forgave web1 alone: the node meant to
// be held to account was the one excused, and a failure of any other was held
// against the step. DIVERGENCE 5.502.
func TestToleratedFailuresUseSaltsGlob(t *testing.T) {
	failed := []string{"web1.prod", "web2.prod", "db1.prod"}
	for _, c := range []struct {
		patterns []string
		want     []string // what is left to block the step
	}{
		{[]string{"web[!1]*"}, []string{"web1.prod", "db1.prod"}},
		{[]string{"[!d]*"}, []string{"db1.prod"}},
		{[]string{"web*"}, []string{"db1.prod"}},
		{[]string{"web1.prod", "db1.*"}, []string{"web2.prod"}},
		{nil, failed},
	} {
		if got := excludeTolerated(failed, c.patterns); !reflect.DeepEqual(got, c.want) {
			t.Errorf("tolerating %v leaves %v, want %v", c.patterns, got, c.want)
		}
	}
}
