package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/value"
)

// `reactor.test` judges a caller reaction against the node that would fire it.
//
// A `caller.` reaction runs on the node that fired the event, and the real
// dispatch authorizes that node by its name. The plan asked the policy about a
// placeholder, "<the node that fired the event>", which no node-scoped rule
// matches: for a role limited to `web*.example`, `reactor.test` said "denied"
// about a reaction that the reactor would then dispatch. The preview exists so
// an operator learns a reaction would be refused before three in the morning;
// this one said refused when it was not. DIVERGENCE 5.498.
func TestReactorTestAndTheReactorAgreeOnACallerReaction(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"restart.sls": "restart_it:\n  caller.service.restart:\n    - arg:\n      - nginx\n",
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls: ['$DIR/restart.sls']
    principal: 'reactor:heal'
`)
	healer, _, err := policy.Load([]byte(`
roles:
  heal:
    - target: 'web*.example'
      functions: ['service.restart']
  administrator:
    - target: '*'
      functions: ['*', 'cmd.run']
    - target: '*'
      runners: ['*']
bindings:
  - principal: 'reactor:heal'
    roles: ['heal']
  - principal: 'cert:CN=ed'
    roles: ['administrator']
`), "heal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = healer
	l.enrolled(t, "web1.example")
	op := l.operator(t, "ed")

	// What the preview says.
	res, err := op.Runner(context.Background(), runnerRequest("reactor.test", map[string]any{
		"tag":  "halite/beacon/web1.example/inotify/etc/nginx",
		"node": "web1.example",
		"data": map[string]any{"id": "web1.example"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	out := mapOf(t, returned(t, res))
	raw, _ := out.Get("reactions")
	list, ok := raw.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("the plan is %v", raw)
	}
	auth, _ := mapOf(t, list[0]).Get("authorized")
	planned := value.KeyString(auth)

	// What the reactor then does with the same event.
	handled := make(chan []ReactionResult, 1)
	r.Handled = func(_ string, res []ReactionResult) { handled <- res }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)
	r.Offer(&eventbus.Event{
		Tag: "halite/beacon/web1.example/inotify/etc/nginx", Node: "web1.example",
		Data: map[string]any{"id": "web1.example"},
	})
	var did []ReactionResult
	select {
	case did = <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("the reactor never handled the event")
	}
	dispatched := len(did) == 1 && did[0].Succeeded()

	if !dispatched {
		t.Fatalf("the reactor did not dispatch the reaction: %+v", did)
	}
	if strings.Contains(planned, "denied") {
		t.Errorf("reactor.test says %q about a reaction the reactor dispatched", planned)
	}
}

// With no node, the plan says it cannot say; and for a node the role does not
// cover it says denied, as the reactor would.
func TestReactorTestSaysWhatItCannotKnowAboutACallerReaction(t *testing.T) {
	l, _, _ := reactorLab(t, map[string]string{
		"restart.sls": "restart_it:\n  caller.service.restart:\n    - arg:\n      - nginx\n",
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls: ['$DIR/restart.sls']
    principal: 'reactor:heal'
`)
	healer, _, err := policy.Load([]byte(`
roles:
  heal:
    - target: 'web*.example'
      functions: ['service.restart']
  administrator:
    - target: '*'
      functions: ['*', 'cmd.run']
    - target: '*'
      runners: ['*']
bindings:
  - principal: 'reactor:heal'
    roles: ['heal']
  - principal: 'cert:CN=ed'
    roles: ['administrator']
`), "heal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = healer
	op := l.operator(t, "ed")

	plan := func(node string) string {
		t.Helper()
		args := map[string]any{"tag": "halite/beacon/x/y"}
		if node != "" {
			args["node"] = node
		}
		res, err := op.Runner(context.Background(), runnerRequest("reactor.test", args))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := mapOf(t, returned(t, res)).Get("reactions")
		list, ok := raw.([]any)
		if !ok || len(list) != 1 {
			t.Fatalf("the plan is %v", raw)
		}
		auth, _ := mapOf(t, list[0]).Get("authorized")
		return value.KeyString(auth)
	}
	if got := plan("db1.example"); !strings.Contains(got, "denied") {
		t.Errorf("a node outside the role: %q, want denied", got)
	}
	if got := plan(""); !strings.Contains(got, "not known") {
		t.Errorf("no node given: %q, want it to say it cannot know", got)
	}
}
