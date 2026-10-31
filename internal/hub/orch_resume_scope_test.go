package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// Resuming a run is held to the same visibility as showing it.
//
// `state.orch_show` and `state.orch_list` tell a caller scoped to a few nodes
// that another principal's run does not exist (issue 321, M-56): a run's
// timeline holds the nodes every step reached. `state.orch_resume` took any
// jid it was handed. It read the run, resumed it, and returned the new run's
// timeline, in which each step carried forward from the old one reads
// "Carried forward from <jid>: <that step's comment>" -- the old run's own text,
// handed to a caller who was told, one call earlier, that there was no such run.
// DIVERGENCE 5.504.
func TestAScopedCallerCannotResumeARunItCannotSee(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'

deploy_web:
  salt.state:
    - tgt: '*'
    - sls:
      - webserver
    - require:
      - salt: drain
`})
	p, _, err := policy.Load([]byte(`
roles:
  scoped:
    - target: 'web*.example'
      runners: ['state.orch_show', 'state.orch_resume']
  administrator:
    - target: '*'
      functions: ['*']
    - target: '*'
      runners: ['*']
bindings:
  - principal: 'cert:CN=alice'
    roles: ['scoped']
  - principal: 'cert:CN=ed'
    roles: ['administrator']
`), "orch-scope.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = p
	defer l.answering(t, "web1.example", nil)()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	alice := l.operator(t, "alice")

	// Shown nothing, as before.
	shown, err := alice.Runner(context.Background(), transport.RunnerRequest{
		Fun: "state.orch_show", Arg: []string{string(run.JID)},
	})
	if err == nil && shown.Success {
		t.Fatalf("alice was shown another principal's run: %s", string(shown.Return))
	}
	// And not allowed to resume it either.
	res, err := alice.Runner(context.Background(), transport.RunnerRequest{
		Fun: "state.orch_resume", Arg: []string{string(run.JID), "deploy_web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success || strings.Contains(string(res.Return), "Carried forward") {
		t.Fatalf("alice resumed a run she cannot see: success=%v %s", res.Success, string(res.Return))
	}
	if res.Error != shown.Error {
		t.Errorf("the refusal to resume (%q) is not the one orch_show gives (%q)", res.Error, shown.Error)
	}
}

// `state.orch_list limit=N` lists up to N of the runs the caller can see, as
// jobs.list_jobs does (DIVERGENCE 5.503): the limit cut the store to the most
// recent N runs of everybody before the caller's visibility did.
func TestAScopedCallerIsListedItsOwnRunsUpToTheLimit(t *testing.T) {
	l := orchLab(t, map[string]string{"x.sls": "a:\n  salt.runner:\n    - name: manage.status\n"})
	p, _, err := policy.Load([]byte(`
roles:
  scoped:
    - target: 'web*.example'
      runners: ['state.orch_list']
bindings:
  - principal: 'cert:CN=alice'
    roles: ['scoped']
`), "orch-list.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = p

	mine := l.server.clock().Next()
	if err := l.server.orchStore().Put(&OrchRun{JID: mine, Principal: "cert:CN=alice", State: OrchComplete}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if err := l.server.orchStore().Put(&OrchRun{
			JID: l.server.clock().Next(), Principal: "cert:CN=ed", State: OrchComplete,
		}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := l.operator(t, "alice").Runner(context.Background(), transport.RunnerRequest{
		Fun: "state.orch_list", Kwarg: map[string]any{"limit": 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := mapOf(t, returned(t, res))
	if _, ok := listed.Get(string(mine)); !ok {
		t.Errorf("alice's own run is not in state.orch_list limit=20: it listed %d runs", listed.Len())
	}
}
