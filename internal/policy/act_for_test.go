package policy

import (
	"strings"
	"testing"
)

// `act_for` names the principals a caller may tell the hub it acts for,
// and grants nothing else (issue 618).
func TestActForNamesWhomACallerMayActFor(t *testing.T) {
	p, _, err := Load([]byte(`
roles:
  api-service:
    - target: '*'
      runners: ['jobs.*']
    - act_for: ['local:*', 'oidc:group:sre']
  operator:
    - target: '*'
      runners: ['*']
bindings:
  - principal: 'cert:CN=api'
    roles: ['api-service']
  - principal: 'cert:CN=ed'
    roles: ['operator']
`), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		caller, operator string
		want             bool
	}{
		{"cert:CN=api", "local:alice", true},
		{"cert:CN=api", "oidc:group:sre", true},
		{"cert:CN=api", "oidc:group:dba", false},
		{"cert:CN=api", "cert:CN=ed", false},
		{"cert:CN=api", "", false},
		// Every runner, on every node, and no act_for: not the same grant.
		{"cert:CN=ed", "local:alice", false},
		{"cert:CN=nobody", "local:alice", false},
	} {
		if got := p.MayActFor(c.caller, c.operator); got != c.want {
			t.Errorf("MayActFor(%q, %q) = %v, want %v", c.caller, c.operator, got, c.want)
		}
	}
	var nilPolicy *Policy
	if nilPolicy.MayActFor("cert:CN=api", "local:alice") {
		t.Error("no policy let a caller act for another")
	}
	// A rule granting only act_for grants no runner and no function.
	if d := p.Authorize(Request{Principal: "cert:CN=api", Fun: "key.accept", Runner: true}); d.Allowed {
		t.Errorf("act_for granted a runner: %v", d)
	}
}

// A target on a rule that grants only act_for would read as a limit it is
// not, and would show the caller every node it covers on the event stream.
func TestActForWithATargetIsRefusedAtLoad(t *testing.T) {
	_, _, err := Load([]byte(`
roles:
  api-service:
    - target: '*'
      act_for: ['local:*']
`), "policy.yaml")
	if err == nil || !strings.Contains(err.Error(), "act_for") {
		t.Errorf("a target on an act_for rule loaded: %v", err)
	}
}
