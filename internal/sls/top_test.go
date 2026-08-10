package sls

import "testing"

func TestTargetMatch(t *testing.T) {
	grains := map[string]any{
		"id":        "web1",
		"os_family": "Debian",
		"nilgrain":  nil,
	}
	cases := []struct {
		pat  string
		want bool
	}{
		{"*", true},
		{"web*", true},
		{"db*", false},
		{"os_family:Debian", true},
		{"os_family:Deb*", true},
		{"os_family:RedHat", false},
		// A grain the host does not have matches nothing, not even a glob.
		{"role:*", false},
		{"role:web", false},
		{"nilgrain:*", false},
		{"nope:<nil>", false},
	}
	for _, c := range cases {
		if got := TargetMatch(c.pat, grains); got != c.want {
			t.Errorf("TargetMatch(%q) = %v, want %v", c.pat, got, c.want)
		}
	}
}
