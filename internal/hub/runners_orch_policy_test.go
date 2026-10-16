package hub

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// allow_mods has to judge the orchestration the runner will compile,
// not the positional arguments the request happened to carry. The
// runner binds `sls` by keyword as readily as by position, and splits
// it on commas, so a policy that read only the positional arguments
// allowed `halite-hub orch run teardown.everything` -- which sends the
// name as a keyword -- to a role limited to `deploy.*`, and allowed
// `deploy.web,teardown.everything` as one positional argument because
// `deploy.*` matches the whole string. DIVERGENCE 5.319.
//
// This drives CallRunner, the path an operator, a reaction and the API
// all reach, against a tree that has both orchestrations, and checks
// what was compiled as well as what was decided: a refusal that said
// "denied" while the runner had run would be no better.
func TestAllowModsJudgesTheOrchestrationTheRunnerCompiles(t *testing.T) {
	step := func(id string) string {
		return id + ":\n  salt.function:\n    - name: test.ping\n    - tgt: '*'\n"
	}
	l := orchLab(t, map[string]string{
		"deploy/web.sls":          step("deploy_web"),
		"teardown/everything.sls": step("teardown_everything"),
	})

	loaded, _, err := policy.Load([]byte(`
roles:
  deployer:
    - runners: ['state.orchestrate', 'state.orch', 'state.orchestrate_show_sls']
      args:
        'state.orchestrate':
          allow_mods: ['deploy.*']
        'state.orch':
          allow_mods: ['deploy.*']
        'state.orchestrate_show_sls':
          allow_mods: ['deploy.*']
bindings:
  - principal: 'cert:CN=ci-pipeline'
    roles: ['deployer']
`), "orch-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded

	cases := []struct {
		name      string
		arg       []string
		kwarg     map[string]any
		wantAllow bool
	}{
		{"an allowed name by position", []string{"deploy.web"}, nil, true},
		// What `halite-hub orch run deploy.web` sends.
		{"an allowed name by keyword", nil, map[string]any{"sls": "deploy.web"}, true},
		{"a refused name by position", []string{"teardown.everything"}, nil, false},
		// What `halite-hub orch run teardown.everything` sends.
		{"a refused name by keyword", nil, map[string]any{"sls": "teardown.everything"}, false},
		{"a refused name in a comma list by position", []string{"deploy.web,teardown.everything"}, nil, false},
		// What `halite-hub orch run deploy.web teardown.everything` sends.
		{"a refused name in a comma list by keyword", nil, map[string]any{"sls": "deploy.web,teardown.everything"}, false},
		{"a refused name behind a space in a comma list", nil, map[string]any{"sls": "deploy.web, teardown.everything"}, false},
		{"no name at all", nil, map[string]any{"sls": " , "}, false},
		{"sls as a list", nil, map[string]any{"sls": []any{"teardown.everything"}}, false},
	}
	for _, fun := range []string{"state.orchestrate", "state.orch", "state.orchestrate_show_sls"} {
		for _, tc := range cases {
			t.Run(fun+"/"+tc.name, func(t *testing.T) {
				kwarg := map[string]any{}
				for k, v := range tc.kwarg {
					kwarg[k] = v
				}
				if fun != "state.orchestrate_show_sls" {
					// Report rather than dispatch; there is no node.
					kwarg["test"] = true
				}
				out, err := l.server.CallRunner(context.Background(), RunnerCall{
					Principal: "cert:CN=ci-pipeline", Fun: fun, Arg: tc.arg, Kwarg: kwarg,
				})
				var refusal *policyRefusal
				refused := errors.As(err, &refusal)
				if tc.wantAllow {
					if err != nil {
						t.Fatalf("refused: %v", err)
					}
					if !out.Success {
						t.Fatalf("the runner failed: %s", out.Err)
					}
					got := compiledSLS(t, out.Return)
					if strings.Join(got, ",") != "deploy.web" {
						t.Errorf("compiled %v, want [deploy.web]", got)
					}
					return
				}
				if !refused {
					// Say what ran, which is the point of the test.
					if err == nil {
						t.Fatalf("allowed, and the runner compiled %v", compiledSLS(t, out.Return))
					}
					t.Fatalf("not refused by the policy (the runner said %v)", err)
				}
			})
		}
	}
}

// compiledSLS reads the orchestration names a runner's return says it
// compiled. orchestrate, orch and orchestrate_show_sls all carry `sls`.
func compiledSLS(t *testing.T, ret any) []string {
	t.Helper()
	m, ok := ret.(*value.Map)
	if !ok {
		t.Fatalf("the return is %T", ret)
	}
	raw, _ := m.Get("sls")
	list, _ := raw.([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, value.KeyString(v))
	}
	return out
}

// The policy's reading of a runner request (internal/policy runnerNames:
// every positional argument and the keyword `sls`) is only sound while
// every runner that takes orchestration names takes them as a string
// parameter called `sls`. A runner added later that took them as
// `mods`, or as a list, would be judged on nothing. This holds the two
// paths together: if it fails, runnerNames has to learn the new shape
// before the runner ships.
func TestEveryRunnerThatTakesOrchestrationNamesTakesThemAsSLS(t *testing.T) {
	reg := NewRunners()
	for _, name := range reg.Names() {
		sig, _ := reg.sigs.Lookup(name)
		for i, p := range sig.Params {
			switch p.Name {
			case "sls":
				if i != 0 || p.Type != signature.String || p.KeywordOnly {
					t.Errorf("%s takes `sls` as parameter %d of type %s (keyword only: %v); "+
						"the policy judges it as the first positional string or the keyword",
						name, i, p.Type, p.KeywordOnly)
				}
			case "mods", "names", "orchestration":
				t.Errorf("%s takes orchestration names as %q, which the policy does not judge", name, p.Name)
			}
		}
	}
}
