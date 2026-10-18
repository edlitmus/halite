package policy

import (
	"strings"
	"testing"
)

// **A policy list item that is not a string is an error, not dropped**
// (issue 321, section 5). `deny_kwargs: [pillar, 5]` denied pillar only;
// `functions: [on]`, a YAML bool, granted nothing; a mapping where a list
// was meant left the field empty. Each loaded without complaint, and a
// deny list was weaker than written.
func TestAPolicyListOfNonStringsIsRefused(t *testing.T) {
	head := "roles:\n  ops:\n    - target: 'web*'\n"
	for _, c := range []struct{ name, body string }{
		{"a number in deny_kwargs", head + "      functions: ['state.apply']\n      args:\n        'state.apply':\n          deny_kwargs: [pillar, 5]\n"},
		{"a bool in functions", head + "      functions: [true]\n"},
		{"a mapping for functions", head + "      functions: {state.apply: true}\n"},
		{"a number in roles", head + "      functions: ['test.ping']\nbindings:\n  - principal: 'cert:CN=x'\n    roles: [ops, 7]\n"},
	} {
		_, _, err := Load([]byte(c.body), "policy.yaml")
		if err == nil {
			t.Errorf("%s: the policy loaded", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "policy.yaml") {
			t.Errorf("%s: the error does not say where: %v", c.name, err)
		}
	}
	// And a well-formed one still loads.
	ok := head + "      functions: ['state.apply', 'test.ping']\n      args:\n        'state.apply':\n          deny_kwargs: [pillar]\n"
	if _, _, err := Load([]byte(ok), "policy.yaml"); err != nil {
		t.Errorf("a well-formed policy was refused: %v", err)
	}
}
