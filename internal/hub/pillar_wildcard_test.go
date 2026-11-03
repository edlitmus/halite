package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// pillarWildcardPolicy grants star everything by `*` and named the
// pillar readers by name, each for a person and for a reactor.
func pillarWildcardPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	p, _, err := policy.Load([]byte(`
roles:
  star:
    - target: '*'
      functions: ['*']
    - target: '*'
      runners: ['*']
  named:
    - target: '*'
      functions: ['pillar.items', 'pillar.get', 'config.get']
    - target: '*'
      runners: ['pillar.show_pillar', 'reactor.test']
bindings:
  - principal: 'cert:CN=star'
    roles: [star]
  - principal: 'reactor:star'
    roles: [star]
  - principal: 'cert:CN=named'
    roles: [named]
  - principal: 'reactor:named'
    roles: [named]
`), "pillar-wildcard.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// **No route through the hub reads pillar on a wildcard grant**
// (DIVERGENCE 5.558). The hub's CallRunner asked the policy without the
// flag that `GET /v1/pillar/{id}` set, so `halite-hub runner
// pillar.show_pillar`, a reactor's `runner.` reaction and an
// orchestration's `salt.runner` step were all granted it by
// `runners: ['*']`; and a job of `pillar.items` was granted by
// `functions: ['*']`. Each is asked here with a wildcard grant, which is
// refused, and with the function named, which is not.
func TestNoHubRouteReadsPillarOnAWildcard(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = pillarWildcardPolicy(t)
	l.enrolled(t, "web1.example")
	star := l.operator(t, "star")
	named := l.operator(t, "named")
	ctx := context.Background()

	// The operator's runner call: `halite-hub runner`, and what the API
	// forwards.
	_, err := star.Runner(ctx, transport.RunnerRequest{Fun: "pillar.show_pillar", Arg: []string{"web1.example"}})
	if err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Errorf("runners: ['*'] ran pillar.show_pillar: %v", err)
	}
	// A refusal by policy is an error; a runner that ran and found no
	// grains for a node that never connected is an answer. Either is
	// fine here, as long as it is not the first.
	if _, err := named.Runner(ctx, transport.RunnerRequest{Fun: "pillar.show_pillar", Arg: []string{"web1.example"}}); err != nil {
		t.Errorf("pillar.show_pillar, named, was refused: %v", err)
	}
	// The wildcard is still a wildcard for what does not read pillar.
	if _, err := star.Runner(ctx, transport.RunnerRequest{Fun: "cache.grains", Arg: []string{"web1.example"}}); err != nil {
		t.Errorf("runners: ['*'] no longer grants cache.grains: %v", err)
	}

	// A job sent to the node.
	for _, fun := range []string{"pillar.items", "pillar.get", "config.get"} {
		sub := transport.SubmitRequest{Target: "web1.example", TargetKind: "L", Fun: fun}
		if fun != "pillar.items" {
			sub.Arg = []string{"db:password"}
		}
		if _, err := star.Submit(ctx, sub); err == nil || !strings.Contains(err.Error(), "wildcard") {
			t.Errorf("functions: ['*'] sent %s: %v", fun, err)
		}
		if _, err := named.Submit(ctx, sub); err != nil {
			t.Errorf("%s, named, was refused: %v", fun, err)
		}
	}
}

// A reactor's runner reaction, and the preview of it, agree that a
// wildcard does not reach pillar.show_pillar.
func TestAReactorCannotReadPillarOnAWildcard(t *testing.T) {
	for _, c := range []struct {
		principal string
		refused   bool
	}{
		{"reactor:star", true},
		{"reactor:named", false},
	} {
		t.Run(c.principal, func(t *testing.T) {
			l, r, _ := reactorLab(t, map[string]string{
				"leak.sls": "read_it:\n  runner.pillar.show_pillar:\n    - args:\n        node: web1.example\n",
			}, `
reactor:
  - tag: 'halite/beacon/**'
    sls: ['$DIR/leak.sls']
    principal: '`+c.principal+`'
`)
			l.server.Policy = pillarWildcardPolicy(t)
			l.enrolled(t, "web1.example")

			res, err := l.operator(t, "named").Runner(context.Background(), runnerRequest("reactor.test", map[string]any{
				"tag":  "halite/beacon/web1.example/x",
				"node": "web1.example",
				"data": map[string]any{"id": "web1.example"},
			}))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := mapOf(t, returned(t, res)).Get("reactions")
			list, ok := raw.([]any)
			if !ok || len(list) != 1 {
				t.Fatalf("the plan is %v", raw)
			}
			auth, _ := mapOf(t, list[0]).Get("authorized")
			planned := value.KeyString(auth)
			if strings.Contains(planned, "wildcard") != c.refused {
				t.Errorf("reactor.test says %q", planned)
			}

			handled := make(chan []ReactionResult, 1)
			r.Handled = func(_ string, res []ReactionResult) { handled <- res }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.start(ctx)
			r.Offer(&eventbus.Event{
				Tag: "halite/beacon/web1.example/x", Node: "web1.example",
				Data: map[string]any{"id": "web1.example"},
			})
			var did []ReactionResult
			select {
			case did = <-handled:
			case <-time.After(5 * time.Second):
				t.Fatal("the reactor never handled the event")
			}
			if len(did) != 1 {
				t.Fatalf("the reactor did %+v", did)
			}
			if strings.Contains(did[0].Error, "wildcard") != c.refused {
				t.Errorf("the reaction's outcome: %q", did[0].Error)
			}
		})
	}
}
