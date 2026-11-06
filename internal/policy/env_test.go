package policy

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/builtin"
)

// allow_env and deny_env (issue 620): the environment a job or an
// orchestration compiles from, judged as an argument rule and held across
// the function's family like allow_sls.
func TestAllowEnvJudgesTheEnvironmentARequestRunsIn(t *testing.T) {
	p, _, err := Load([]byte(`
roles:
  release:
    - target: '*'
      functions: ['state.*']
      args:
        'state.apply':
          allow_env: ['base', 'release-*']
          deny_env: ['release-bad']
    - runners: ['state.orchestrate', 'state.orch', 'state.orch_resume']
      args:
        'state.orchestrate':
          allow_env: ['release-*']
bindings:
  - principal: 'cert:CN=rel'
    roles: ['release']
`), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	reg := builtin.New()
	p.UseCatalog(reg.Exec.Signatures(), reg.States.Signatures())

	job := func(fun, env string) Request {
		return Request{Principal: "cert:CN=rel", Target: "web1", Fun: fun, Arg: []string{"web"}, Env: env}
	}
	runner := func(fun string, arg []string, kwarg map[string]any) Request {
		return Request{Principal: "cert:CN=rel", Fun: fun, Arg: arg, Kwarg: kwarg, Runner: true}
	}
	resume := func(env string, names []string) Request {
		r := runner("state.orch_resume", []string{"20261105T000000000000", "step"}, nil)
		r.Env, r.Resumes = env, names
		return r
	}
	cases := []struct {
		name  string
		req   Request
		allow bool
		say   string
	}{
		{"a job in an allowed environment", job("state.apply", "release-7"), true, ""},
		{"a job from another branch", job("state.apply", "attacker"), false, `environment "attacker"`},
		{"a job in a denied environment the allow list matches", job("state.apply", "release-bad"), false, `"release-bad"`},
		// The node's own configured environment, which the caller did not
		// choose: the node's env_allowlist is what limits it.
		{"a job that names no environment", job("state.apply", ""), true, ""},
		// Held by the family, as allow_sls is.
		{"a sibling from another branch", job("state.sls", "attacker"), false, "state.apply"},
		{"an orchestration by keyword", runner("state.orchestrate", nil, map[string]any{"sls": "x", "env": "release-1"}), true, ""},
		{"an orchestration from a branch by keyword", runner("state.orch", nil, map[string]any{"sls": "x", "env": "attacker"}), false, `"attacker"`},
		{"an orchestration from a branch by position", runner("state.orchestrate", []string{"x", "attacker"}, nil), false, `"attacker"`},
		// No env compiles from base, which this role does not allow.
		{"an orchestration that names no environment", runner("state.orchestrate", nil, map[string]any{"sls": "x"}), false, `"base"`},
		{"an env that is not a string", runner("state.orchestrate", nil, map[string]any{"sls": "x", "env": []any{"release-1"}}), false, "cannot check"},
		// The first question about a resume knows neither its names nor
		// its environment; the hub asks again with both.
		{"a resume before its run is read", resume("", nil), true, ""},
		{"a resume of a run in an allowed environment", resume("release-2", []string{"x"}), true, ""},
		{"a resume of a run from a branch", resume("attacker", []string{"x"}), false, `"attacker"`},
	}
	for _, tc := range cases {
		d := p.Authorize(tc.req)
		if d.Allowed != tc.allow {
			t.Errorf("%s: allowed=%v, want %v (%s)", tc.name, d.Allowed, tc.allow, d.Reason)
			continue
		}
		if !tc.allow && !strings.Contains(d.Reason, tc.say) {
			t.Errorf("%s: refused, but the reason does not say %q: %s", tc.name, tc.say, d.Reason)
		}
	}
}
