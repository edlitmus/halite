package policy

import (
	"strings"
	"testing"
)

const example = `
roles:
  webops:
    - target: 'web*.prod'
      functions:
        - 'state.apply'
        - 'service.*'
        - 'pkg.installed'
      args:
        'state.apply':
          allow_sls: ['webserver.*']
          deny_kwargs: ['pillar']
    - target: 'web*.prod'
      functions: ['test.ping', 'grains.items']

  readonly:
    - target: '*'
      functions: ['test.ping', 'grains.*', 'sys.*', 'state.show_*']

  everything:
    - target: '*'
      functions: ['*']

  deployer:
    - runners: ['state.orchestrate']
      args:
        'state.orchestrate':
          allow_mods: ['deploy.*']

bindings:
  - principal: 'cert:CN=alice'
    roles: ['webops', 'readonly']
  - principal: 'cert:CN=ci-pipeline'
    roles: ['deployer']
  - principal: 'cert:CN=root'
    roles: ['everything']
  - principal: 'node:lb*.prod'
    roles: ['readonly']
`

func load(t *testing.T) *Policy {
	t.Helper()
	p, warnings, err := Load([]byte(example), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warnings {
		t.Logf("warning: %v", w)
	}
	p.ArbitraryCode = map[string]bool{
		"cmd.run": true, "cmd.script": true, "cmd.shell": true, "module.run": true,
	}
	return p
}

func TestDenyByDefault(t *testing.T) {
	p := load(t)
	// A principal nobody bound.
	d := p.Authorize(Request{Principal: "cert:CN=nobody", Target: "*", Fun: "test.ping"})
	if d.Allowed {
		t.Error("an unbound principal was allowed")
	}
	if !strings.Contains(d.Reason, "no role") {
		t.Errorf("the reason should say what is missing: %s", d.Reason)
	}
	// An empty policy grants nothing, including to whoever is running
	// the command line.
	empty, _, err := Load([]byte(""), "empty.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Authorize(Request{Principal: "cert:CN=root", Target: "*", Fun: "test.ping"}).Allowed {
		t.Error("an empty policy allowed something")
	}
	// And no policy at all is not an accidental grant.
	var absent *Policy
	if absent.Authorize(Request{Principal: "cert:CN=root", Fun: "test.ping"}).Allowed {
		t.Error("a nil policy allowed something")
	}
}

func TestTargetAndFunctionAreAuthorizedTogether(t *testing.T) {
	p := load(t)
	cases := []struct {
		principal, target, fun string
		want                   bool
	}{
		{"cert:CN=alice", "web1.prod", "state.apply", true},
		{"cert:CN=alice", "web1.prod", "service.restart", true},
		// The right function against the wrong target.
		{"cert:CN=alice", "db1.prod", "state.apply", false},
		// The right target and a function nobody granted.
		{"cert:CN=alice", "web1.prod", "user.delete", false},
		// readonly covers every target, so this passes on the second
		// role rather than the first.
		{"cert:CN=alice", "db1.prod", "test.ping", true},
		{"cert:CN=alice", "db1.prod", "grains.items", true},
		// A glob principal in the binding.
		{"node:lb1.prod", "*", "test.ping", true},
		{"node:lb1.staging", "*", "test.ping", false},
	}
	for _, c := range cases {
		d := p.Authorize(Request{Principal: c.principal, Target: c.target, Fun: c.fun})
		if d.Allowed != c.want {
			t.Errorf("%s %s against %s: allowed=%v (%s)", c.principal, c.fun, c.target, d.Allowed, d.Reason)
		}
	}
}

// Salt's `.*` grants everything, and everybody's Salt ACL grants `.*`.
func TestAWildcardNeverGrantsArbitraryCode(t *testing.T) {
	p := load(t)
	for _, fun := range []string{"cmd.run", "cmd.script", "cmd.shell", "module.run"} {
		d := p.Authorize(Request{Principal: "cert:CN=root", Target: "*", Fun: fun})
		if d.Allowed {
			t.Errorf("a wildcard granted %s", fun)
		}
		if !strings.Contains(d.Reason, "arbitrary code") {
			t.Errorf("%s: the reason should say why: %s", fun, d.Reason)
		}
	}
	// Everything else the wildcard does grant.
	if !p.Authorize(Request{Principal: "cert:CN=root", Target: "*", Fun: "pkg.installed"}).Allowed {
		t.Error("a wildcard should grant an ordinary function")
	}
	// And naming it grants it.
	named, _, err := Load([]byte(`
roles:
  shell:
    - target: 'build*'
      functions: ['cmd.run']
bindings:
  - principal: 'cert:CN=ci'
    roles: ['shell']
`), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	named.ArbitraryCode = map[string]bool{"cmd.run": true}
	if !named.Authorize(Request{Principal: "cert:CN=ci", Target: "build1", Fun: "cmd.run"}).Allowed {
		t.Error("naming cmd.run should grant it")
	}
}

func TestArgumentConstraints(t *testing.T) {
	p := load(t)
	allowed := p.Authorize(Request{
		Principal: "cert:CN=alice", Target: "web1.prod", Fun: "state.apply",
		Arg: []string{"webserver.nginx"},
	})
	if !allowed.Allowed {
		t.Errorf("an SLS inside allow_sls was refused: %s", allowed.Reason)
	}
	refused := p.Authorize(Request{
		Principal: "cert:CN=alice", Target: "web1.prod", Fun: "state.apply",
		Arg: []string{"database.primary"},
	})
	if refused.Allowed {
		t.Error("an SLS outside allow_sls was applied")
	}
	// Passing pillar on the command line is otherwise a trivial way
	// round pillar-based authorization.
	pillar := p.Authorize(Request{
		Principal: "cert:CN=alice", Target: "web1.prod", Fun: "state.apply",
		Arg: []string{"webserver.nginx"}, Kwarg: map[string]any{"pillar": "{}"},
	})
	if pillar.Allowed {
		t.Error("deny_kwargs did not stop a pillar override")
	}
	if !strings.Contains(pillar.Reason, "pillar") {
		t.Errorf("the reason should name the argument: %s", pillar.Reason)
	}
}

func TestRunnersAreGrantedSeparately(t *testing.T) {
	p := load(t)
	if !p.Authorize(Request{
		Principal: "cert:CN=ci-pipeline", Fun: "state.orchestrate", Runner: true,
		Arg: []string{"deploy.web"},
	}).Allowed {
		t.Error("a granted runner was refused")
	}
	if p.Authorize(Request{
		Principal: "cert:CN=ci-pipeline", Fun: "state.orchestrate", Runner: true,
		Arg: []string{"teardown.everything"},
	}).Allowed {
		t.Error("allow_mods did not constrain the runner")
	}
	// A runner grant is not a function grant.
	if p.Authorize(Request{
		Principal: "cert:CN=ci-pipeline", Target: "*", Fun: "state.orchestrate",
	}).Allowed {
		t.Error("a runner grant authorized a fleet job")
	}
}

// A rule scoped to part of the estate must not be widened by asking for
// all of it.
func TestAScopedRuleDoesNotGrantAWiderTarget(t *testing.T) {
	p := load(t)
	if p.Authorize(Request{Principal: "cert:CN=alice", Target: "*", Fun: "state.apply"}).Allowed {
		t.Error("a rule for web*.prod granted a job against *")
	}
	if p.Authorize(Request{Principal: "cert:CN=alice", Target: "db*.prod", Fun: "state.apply"}).Allowed {
		t.Error("a rule for web*.prod granted a job against db*.prod")
	}
	if !p.Authorize(Request{Principal: "cert:CN=alice", Target: "web*.prod", Fun: "state.apply"}).Allowed {
		t.Error("a rule for web*.prod should grant exactly web*.prod")
	}
}

// A missing field is not a wildcard, and a binding to a role that does
// not exist looks like a grant and is not one.
func TestAPolicyThatWouldMisleadIsRefusedAtLoad(t *testing.T) {
	cases := map[string]string{
		"a rule with functions and no target": `
roles:
  r:
    - functions: ['test.ping']
bindings:
  - principal: 'cert:CN=a'
    roles: ['r']
`,
		"a binding to a role that does not exist": `
roles:
  r:
    - target: '*'
      functions: ['test.ping']
bindings:
  - principal: 'cert:CN=a'
    roles: ['typo']
`,
		"a rule that grants nothing": `
roles:
  r:
    - target: '*'
bindings: []
`,
	}
	for what, src := range cases {
		if _, _, err := Load([]byte(src), "policy.yaml"); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
}
