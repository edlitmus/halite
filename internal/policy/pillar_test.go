package policy

import (
	"strings"
	"testing"
)

// pillarPolicy grants every pillar reader three ways: by `*`, by a
// pattern that matches it, and by name. Each is a separate principal, so
// a decision can only have come from the one rule.
const pillarPolicy = `
roles:
  star:
    - target: '*'
      functions: ['*']
    - target: '*'
      runners: ['*']
  pattern:
    - target: '*'
      functions: ['pillar.*', 'config.*']
    - target: '*'
      runners: ['pillar.*', 'cache.*']
  named:
    - target: '*'
      functions: ['pillar.items', 'pillar.raw', 'pillar.get', 'config.get']
    - target: '*'
      runners: ['pillar.show_pillar', 'cache.pillar']
bindings:
  - principal: 'cert:CN=star'
    roles: [star]
  - principal: 'cert:CN=pattern'
    roles: [pattern]
  - principal: 'cert:CN=named'
    roles: [named]
`

// **Reading a node's pillar is never granted by a wildcard, whoever
// asks** (SPEC 22.1, 23.5). The rule used to be a flag on the request,
// which one caller set: `GET /v1/pillar/{id}` refused `runners: ['*']`,
// and the same runner reached through `POST /v1/run`, the hub's own
// runner endpoint, a reactor or an orchestration was granted by it --
// and `pillar.items` sent to the node was granted by `functions: ['*']`
// everywhere. The rule is the function's now, so the question cannot be
// asked without it. DIVERGENCE 5.563.
func TestReadingPillarIsNeverGrantedByAWildcard(t *testing.T) {
	p, _, err := Load([]byte(pillarPolicy), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type ask struct {
		fun    string
		runner bool
	}
	var asks []ask
	for _, fun := range []string{"pillar.items", "pillar.raw", "pillar.get", "config.get"} {
		asks = append(asks, ask{fun, false})
	}
	for _, fun := range []string{"pillar.show_pillar", "cache.pillar"} {
		asks = append(asks, ask{fun, true})
	}
	for _, a := range asks {
		req := func(principal string) Request {
			r := Request{Principal: principal, Fun: a.fun, Runner: a.runner}
			if a.runner {
				r.Arg = []string{"db1"}
			} else {
				r.Target = "db1"
			}
			return r
		}
		for _, principal := range []string{"cert:CN=star", "cert:CN=pattern"} {
			d := p.Authorize(req(principal))
			if d.Allowed {
				t.Errorf("%s was granted %s by a pattern: %s", principal, a.fun, d)
				continue
			}
			if !strings.Contains(d.Reason, "pillar") || !strings.Contains(d.Reason, "name it") {
				t.Errorf("%s refused %s without saying why: %s", principal, a.fun, d.Reason)
			}
		}
		if d := p.Authorize(req("cert:CN=named")); !d.Allowed {
			t.Errorf("naming %s did not grant it: %s", a.fun, d.Reason)
		}
	}

	// The wildcard still grants what does not read pillar: the rule is
	// about these functions, not about the roles that hold `*`.
	for _, r := range []Request{
		{Principal: "cert:CN=star", Target: "db1", Fun: "grains.items"},
		{Principal: "cert:CN=star", Target: "db1", Fun: "pillar.keys"},
		{Principal: "cert:CN=star", Fun: "pillar.show_top", Arg: []string{"db1"}, Runner: true},
		{Principal: "cert:CN=star", Fun: "cache.grains", Arg: []string{"db1"}, Runner: true},
	} {
		if d := p.Authorize(r); !d.Allowed {
			t.Errorf("the wildcard no longer grants %s: %s", r.Fun, d.Reason)
		}
	}
}

// The scope is the second half of a runner's authorization, and it has
// to agree with the first. A principal holding both `runners: ['*']` on
// every node and `pillar.show_pillar` named on web* is granted the
// runner by the named rule -- and if the scope then took its targets
// from every rule matching the name, the wildcard's `*` would hand it
// db1's pillar after all. Every caller passed `false` for the flag that
// was meant to stop that.
func TestAWildcardDoesNotWidenThePillarScope(t *testing.T) {
	p, _, err := Load([]byte(`
roles:
  star:
    - target: '*'
      runners: ['*']
  web:
    - target: 'web*'
      runners: ['pillar.show_pillar']
bindings:
  - principal: 'cert:CN=both'
    roles: [star, web]
`), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Authorize(Request{Principal: "cert:CN=both", Fun: "pillar.show_pillar", Arg: []string{"web1"}, Runner: true}); !d.Allowed {
		t.Fatalf("the named rule did not grant the runner: %s", d.Reason)
	}
	sc := p.RunnerScope("cert:CN=both", "pillar.show_pillar")
	if sc.Covers("db1") || sc.Everything() {
		t.Errorf("runners: ['*'] widened pillar.show_pillar's scope to %v", sc.Targets)
	}
	if !sc.Covers("web1") {
		t.Errorf("the named rule's own target is out of scope: %v", sc.Targets)
	}
	// An ordinary runner is still scoped by the wildcard rule.
	if sc := p.RunnerScope("cert:CN=both", "cache.grains"); !sc.Everything() {
		t.Errorf("cache.grains lost the wildcard's scope: %v", sc.Targets)
	}
}
