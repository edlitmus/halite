package api

import (
	"net/http"
	"testing"
)

// A role's allow_env holds every path through this service that sends a
// job or an orchestration with an environment (issue 620). The API
// authorizes the operator itself before it forwards anything, and the hub
// then judges only this service's certificate, so a question here that
// left the environment out was the only question the operator was asked.
func TestAllowEnvHoldsEveryAPIPathThatChoosesAnEnvironment(t *testing.T) {
	l, hub := executeLab(t, `
roles:
  operator:
    - target: 'web*'
      functions: ['test.ping', 'state.apply']
      args:
        'state.apply':
          allow_env: ['base']
    - runners: ['state.orchestrate', 'jobs.*']
      args:
        'state.orchestrate':
          allow_env: ['base', 'release-*']
bindings:
  - principal: 'local:ed'
    roles: ['operator']
`)
	out := l.login(t, "ed", "hunter2")
	cases := []struct {
		name, path, body string
		want             int
	}{
		{"run in base", PathRun, `{"tgt":"web1","fun":"state.apply","saltenv":"base"}`, http.StatusOK},
		{"run with no environment", PathRun, `{"tgt":"web1","fun":"state.apply"}`, http.StatusOK},
		{"run from a branch", PathRun, `{"tgt":"web1","fun":"state.apply","saltenv":"attacker"}`, http.StatusForbidden},
		// A function with no environment rule is not held by one.
		{"ping from a branch", PathRun, `{"tgt":"web1","fun":"test.ping","saltenv":"attacker"}`, http.StatusOK},
		{"node state in base", "/v1/nodes/web1.example/state", `{"sls":["web"],"saltenv":"base"}`, http.StatusOK},
		{"node state from a branch", "/v1/nodes/web1.example/state", `{"sls":["web"],"saltenv":"attacker"}`, http.StatusForbidden},
		{"orchestration in an allowed environment", "/v1/orch", `{"sls":"deploy","saltenv":"release-7"}`, http.StatusOK},
		// No environment compiles from base, which this role allows.
		{"orchestration with no environment", "/v1/orch", `{"sls":"deploy"}`, http.StatusOK},
		{"orchestration from a branch", "/v1/orch", `{"sls":"deploy","saltenv":"attacker"}`, http.StatusForbidden},
		{"the runner client from a branch", PathRun,
			`{"client":"runner","fun":"state.orchestrate","kwarg":{"sls":"deploy","env":"attacker"}}`, http.StatusForbidden},
		{"the runner client with env by position", PathRun,
			`{"client":"runner","fun":"state.orchestrate","arg":["deploy","attacker"]}`, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(hub.seen)
			res, body := l.post(t, tc.path, tc.body, out.Token)
			if res.StatusCode != tc.want {
				t.Fatalf("answered %d, want %d: %s", res.StatusCode, tc.want, body)
			}
			if tc.want == http.StatusForbidden && len(hub.seen) != before {
				t.Errorf("a refused request still reached the hub: %v", hub.seen[before:])
			}
		})
	}
}
