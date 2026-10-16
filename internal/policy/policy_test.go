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

// allow_mods judges the names the orchestration runner will compile:
// every positional argument and the keyword `sls`, each split on
// commas as the hub splits it. Judging only the positional arguments
// let the keyword through untouched -- and the keyword is the only way
// `halite-hub orch run` sends a name -- and let a comma list through
// whole, because `deploy.*` matches `deploy.web,teardown.everything`.
// DIVERGENCE 5.320.
func TestAllowModsJudgesTheKeywordAndEachNameInACommaList(t *testing.T) {
	p := load(t)
	cases := []struct {
		name       string
		arg        []string
		kwarg      map[string]any
		wantAllow  bool
		wantReason string
	}{
		{"positional, allowed", []string{"deploy.web"}, nil, true, ""},
		{"keyword, allowed", nil, map[string]any{"sls": "deploy.web"}, true, ""},
		{"keyword comma list, all allowed", nil, map[string]any{"sls": "deploy.web,deploy.db"}, true, ""},
		{"keyword, refused", nil, map[string]any{"sls": "teardown.everything"},
			false, "teardown.everything is not in the modules"},
		{"positional comma list, one refused", []string{"deploy.web,teardown.everything"}, nil,
			false, "teardown.everything is not in the modules"},
		{"keyword comma list, one refused", nil, map[string]any{"sls": "deploy.web, teardown.everything"},
			false, "teardown.everything is not in the modules"},
		{"allowed positional, refused keyword", []string{"deploy.web"}, map[string]any{"sls": "teardown.everything"},
			false, "teardown.everything is not in the modules"},
		{"no names", nil, nil, false, "names no orchestration"},
		{"a keyword of commas", nil, map[string]any{"sls": ", ,"}, false, "names no orchestration"},
		{"a keyword that is not a string", nil, map[string]any{"sls": []any{"teardown.everything"}},
			false, "cannot check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := p.Authorize(Request{
				Principal: "cert:CN=ci-pipeline", Fun: "state.orchestrate", Runner: true,
				Arg: tc.arg, Kwarg: tc.kwarg,
			})
			if d.Allowed != tc.wantAllow {
				t.Fatalf("allowed=%v, want %v: %s", d.Allowed, tc.wantAllow, d)
			}
			if !strings.Contains(d.Reason, tc.wantReason) {
				t.Errorf("reason %q does not say %q", d.Reason, tc.wantReason)
			}
		})
	}
}

// deny_sls on a runner is judged on the same names allow_mods is, or
// the keyword and the comma list walk round it as they walked round
// allow_mods. A job's deny_sls is unchanged: the node reads only the
// positional arguments and does not split them.
func TestDenySLSOnARunnerJudgesTheKeywordAndTheCommaList(t *testing.T) {
	p, _, err := Load([]byte(`
roles:
  orch:
    - runners: ['state.orchestrate']
      args:
        'state.orchestrate':
          deny_sls: ['teardown.*']
  web:
    - target: '*'
      functions: ['state.apply']
      args:
        'state.apply':
          deny_sls: ['teardown.*']
bindings:
  - principal: 'cert:CN=ci'
    roles: ['orch', 'web']
`), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		arg       []string
		kwarg     map[string]any
		wantAllow bool
	}{
		{"keyword, not denied", nil, map[string]any{"sls": "deploy.web"}, true},
		{"keyword, denied", nil, map[string]any{"sls": "teardown.everything"}, false},
		{"comma list, denied", []string{"deploy.web,teardown.everything"}, nil, false},
		// deny_sls alone does not require a name: that is allow_mods'
		// rule, and a deny list says nothing about an empty request.
		{"no names", nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := p.Authorize(Request{Principal: "cert:CN=ci", Fun: "state.orchestrate", Runner: true,
				Arg: tc.arg, Kwarg: tc.kwarg})
			if d.Allowed != tc.wantAllow {
				t.Fatalf("allowed=%v, want %v: %s", d.Allowed, tc.wantAllow, d)
			}
		})
	}
	// The job half: `mods` is not a name to the node, and `a,b` is one
	// name to it, so neither is read here.
	job := p.Authorize(Request{Principal: "cert:CN=ci", Target: "web1", Fun: "state.apply",
		Arg: []string{"web"}, Kwarg: map[string]any{"sls": "teardown.everything"}})
	if !job.Allowed {
		t.Errorf("a job's keyword `sls` was judged as a name: %s", job)
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

// A scoped rule is judged against the target as the kind it will be
// dispatched under. Each "allowed: true" row for alice is a request
// whose every reachable node is inside web*.prod; each false row could
// reach a node outside it, or is a kind whose reach a string cannot
// tell. root's `*` rule covers every kind. service.restart, because
// alice's readonly role grants test.ping against `*` and would answer
// every row. DIVERGENCE 5.296: before,
// the rows marked "the escalation" were allowed, because the target
// was matched literally against web*.prod whatever its kind, and `*`
// consumes commas, spaces and `|`.
func TestATargetIsAuthorizedAsTheKindItIsDispatchedUnder(t *testing.T) {
	p := load(t)
	cases := []struct {
		principal, target, kind string
		allowed                 bool
	}{
		// glob, the default: unchanged.
		{"cert:CN=alice", "web1.prod", "", true},
		{"cert:CN=alice", "web1.prod", "glob", true},
		{"cert:CN=alice", "web*.prod", "", true},
		{"cert:CN=alice", "db1.prod", "", false},
		{"cert:CN=alice", "*", "", false},
		// glob: a string that is not a node ID is not a literal.
		{"cert:CN=alice", "web1.prod,db1.prod", "", false},
		{"cert:CN=alice", "web1.prod or db1.prod", "", false},
		{"cert:CN=alice", `web1\.prod`, "", false},

		// list: every element, as a node.
		{"cert:CN=alice", "web1.prod", "L", true},
		{"cert:CN=alice", "web1.prod,web2.prod", "L", true},
		{"cert:CN=alice", "web1.prod, web2.prod", "list", true},
		{"cert:CN=alice", "web1.prod,db1.prod", "L", false}, // the escalation
		{"cert:CN=alice", "web1.prod,db1.prod", "list", false},
		{"cert:CN=alice", "web1.prod, ,db1.prod", "L", false},
		{"cert:CN=alice", "web*.prod", "L", false},
		{"cert:CN=alice", "", "L", false},
		{"cert:CN=alice", ",", "L", false},

		// compound and nodegroup: only `*` covers them.
		{"cert:CN=alice", "web1.prod or db1.prod", "C", false}, // the escalation
		{"cert:CN=alice", "web1.prod and not G@x:y or db1.prod", "compound", false},
		{"cert:CN=alice", "web1.prod", "C", false},
		{"cert:CN=alice", "web*.prod", "C", false},
		{"cert:CN=alice", "webgroup", "N", false},

		// pcre: a pattern however literal it looks.
		{"cert:CN=alice", "web1.prod|db1.prod", "E", false}, // the escalation
		{"cert:CN=alice", "web1.prod", "pcre", false},
		{"cert:CN=alice", "web*.prod", "E", false},

		// grain, pillar, subnet and their regex forms.
		{"cert:CN=alice", "web1.prod", "G", false},
		{"cert:CN=alice", "role:web", "grain", false},
		{"cert:CN=alice", "role:web", "P", false},
		{"cert:CN=alice", "role:web", "I", false},
		{"cert:CN=alice", "role:web", "J", false},
		{"cert:CN=alice", "10.0.0.0/8", "S", false},

		// An unknown kind is never matched.
		{"cert:CN=alice", "web1.prod", "bogus", false},
		{"cert:CN=root", "*", "bogus", false},

		// `*` still covers every kind.
		{"cert:CN=root", "*", "", true},
		{"cert:CN=root", "web1.prod,db1.prod", "L", true},
		{"cert:CN=root", "web1.prod or db1.prod", "C", true},
		{"cert:CN=root", "web1.prod|db1.prod", "E", true},
		{"cert:CN=root", "role:web", "G", true},
		{"cert:CN=root", "webgroup", "N", true},
		{"cert:CN=root", "10.0.0.0/8", "S", true},
	}
	for _, c := range cases {
		d := p.Authorize(Request{
			Principal: c.principal, Target: c.target, TargetKind: c.kind, Fun: "service.restart",
		})
		if d.Allowed != c.allowed {
			t.Errorf("%s: service.restart against %q as kind %q: allowed=%v, want %v (%s)",
				c.principal, c.target, c.kind, d.Allowed, c.allowed, d)
		}
	}
}

// A denial for a non-glob kind says which kind it judged, so an
// operator refused for `-C 'web1.prod'` is not left wondering why a
// glob rule for web*.prod did not match web1.prod.
func TestADenialNamesTheTargetKind(t *testing.T) {
	p := load(t)
	d := p.Authorize(Request{Principal: "cert:CN=alice", Target: "web1.prod", TargetKind: "C", Fun: "state.apply"})
	if d.Allowed || !strings.Contains(d.Reason, "compound") {
		t.Errorf("got %s; want a denial naming the compound kind", d)
	}
	d = p.Authorize(Request{Principal: "cert:CN=alice", Target: "web1.prod", TargetKind: "nope", Fun: "test.ping"})
	if d.Allowed || !strings.Contains(d.Reason, "not a target kind") {
		t.Errorf("got %s; want a denial for an unknown kind", d)
	}
}

// deny_sls and allow_sls are matched against the one spelling of an SLS
// name the node's compiler loads. Every spelling below is a name a file
// server folds to secret/x.sls (DIVERGENCE 5.294), and until the policy
// followed the compiler each was authorized under `deny_sls: [secret.x]`,
// because it was matched as written.
func TestSLSConstraintsJudgeTheCanonicalName(t *testing.T) {
	const src = `
roles:
  ops:
    - target: '*'
      functions: ['state.sls', 'state.apply']
      args:
        'state.sls':
          deny_sls: ['secret.x', 'vault.*']
        'state.apply':
          allow_sls: ['web.*']
bindings:
  - principal: 'cert:CN=ops'
    roles: ['ops']
`
	p, _, err := Load([]byte(src), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ask := func(fun string, args ...string) Decision {
		return p.Authorize(Request{Principal: "cert:CN=ops", Target: "n1", Fun: fun, Arg: args})
	}

	for _, spelling := range []string{
		"secret.x", "secret..x", "secret/x", ".secret.x", "secret/./x",
		"secret.x.", "/secret/x", `secret\x`, "vault/db",
	} {
		d := ask("state.sls", "ok", spelling)
		if d.Allowed {
			t.Errorf("state.sls %q was authorized under deny_sls [secret.x vault.*]", spelling)
		}
	}
	// The reason for a non-canonical name gives the spelling to use.
	if d := ask("state.sls", "secret/x"); !strings.Contains(d.Reason, `"secret.x"`) {
		t.Errorf("the refusal should give the canonical spelling: %s", d.Reason)
	}
	if d := ask("state.sls", "ok", "secrets.x", "web.nginx"); !d.Allowed {
		t.Errorf("canonical names outside deny_sls were refused: %s", d.Reason)
	}

	// allow_sls: a canonical name inside it is allowed; the same file
	// spelled another way is refused rather than matched loosely.
	if d := ask("state.apply", "web.nginx"); !d.Allowed {
		t.Errorf("web.nginx is inside allow_sls [web.*] and was refused: %s", d.Reason)
	}
	for _, spelling := range []string{"web..nginx", "web/nginx", ".web.nginx", "web.nginx/"} {
		if d := ask("state.apply", spelling); d.Allowed {
			t.Errorf("state.apply %q was authorized under allow_sls [web.*]", spelling)
		}
	}
}
