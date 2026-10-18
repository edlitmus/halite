package policy

import "testing"

// A runner rule's target is its scope; a rule with none grants the runner
// and no nodes (issue 321, M-56).
func TestRunnerScopeIsTheTargetsOfTheGrantingRules(t *testing.T) {
	src := `roles:
  web:
    - target: 'web*'
      runners: ['pillar.show_pillar', 'cache.*']
  db:
    - target: 'db1'
      runners: ['pillar.show_pillar']
  bare:
    - runners: ['pillar.show_pillar']
  all:
    - target: '*'
      runners: ['*']
bindings:
  - principal: 'cert:CN=webops'
    roles: [web, db]
  - principal: 'cert:CN=bare'
    roles: [bare]
  - principal: 'cert:CN=admin'
    roles: [all]
`
	p, _, err := Load([]byte(src), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sc := p.RunnerScope("cert:CN=webops", "pillar.show_pillar", false)
	for node, want := range map[string]bool{"web1": true, "webz": true, "db1": true, "db2": false, "mail": false} {
		if got := sc.Covers(node); got != want {
			t.Errorf("webops reaching %s through pillar.show_pillar: %v, want %v", node, got, want)
		}
	}
	if sc.Everything() {
		t.Error("web* and db1 are not the whole estate")
	}
	if got := p.RunnerScope("cert:CN=webops", "cache.grains", false); got.Covers("db1") || !got.Covers("web1") {
		t.Errorf("cache.grains is granted by the web rule alone: %v", got.Targets)
	}
	if got := p.RunnerScope("cert:CN=bare", "pillar.show_pillar", false); got.Covers("web1") || len(got.Targets) != 0 {
		t.Errorf("a runner rule with no target reached %v", got.Targets)
	}
	if got := p.RunnerScope("cert:CN=admin", "key.delete", false); !got.Everything() || !got.Covers("anything") {
		t.Errorf("target '*' is everything: %v", got.Targets)
	}
	// A wildcard does not put a never-by-wildcard runner in scope.
	if got := p.RunnerScope("cert:CN=admin", "key.delete", true); len(got.Targets) != 0 {
		t.Errorf("runners: ['*'] scoped a runner a wildcard never grants: %v", got.Targets)
	}
}

// A scope narrowed by another covers what both cover, and is the whole
// estate only if both are.
func TestAScopeWithinAnotherIsTheirIntersection(t *testing.T) {
	api := Scope{Targets: []string{"*"}}
	op := Scope{Targets: []string{"web*"}}
	both := api.Within(op)
	if !both.Covers("web1") || both.Covers("db1") || both.Everything() {
		t.Errorf("'*' within web*: web1=%v db1=%v everything=%v", both.Covers("web1"), both.Covers("db1"), both.Everything())
	}
	if none := api.Within(Scope{}); none.Covers("web1") {
		t.Error("a scope within an empty one covered a node")
	}
	if !api.Within(api).Everything() {
		t.Error("'*' within '*' is not everything")
	}
}
