package hub

import (
	"context"
	"net/http"
	"testing"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/transport"
)

// Issue 649: PR 647 made the hub judge an API runner call as the
// operator, and record the operator on the jobs and runs it makes. Two
// records still named only the API's certificate: an event sent with
// `event.send`, and the `halite/key/<node>/<action>` audit trail. Both
// tests drive a real api.Server in front of this package's hub, as
// api_delegation_test.go does.

// busEvents reads every event under one tag glob.
func busEvents(t *testing.T, l *lab, glob string) []eventbus.Event {
	t.Helper()
	events, _, err := l.server.Events.Read(eventbus.Earliest, []string{glob}, 100)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// **event.send** names the operator it was sent for, refuses a
// webhook's tag from an operator through the API, and does not take an
// `_on_behalf_of` from the payload.
func TestEventSendThroughTheAPIRecordsTheOperator(t *testing.T) {
	a := newAPILab(t, nil)
	bob := a.login(t, "bob")

	code, _, raw := a.runner(t, bob, "event.send", nil, map[string]any{
		"tag":  "halite/audit/deployed",
		"data": map[string]any{"_on_behalf_of": "local:alice", "_principal": "local:alice"},
	})
	if code != http.StatusOK {
		t.Fatalf("bob could not send an event through the API: %d %s", code, raw)
	}
	sent := busEvents(t, a.lab, "halite/audit/**")
	if len(sent) != 1 {
		t.Fatalf("the bus has %d audit events, want 1", len(sent))
	}
	if who := sent[0].Data["_principal"]; who != "cert:CN=api" {
		t.Errorf("_principal = %v, want the connection's certificate", who)
	}
	if who := sent[0].Data["_on_behalf_of"]; who != "local:bob" {
		t.Errorf("_on_behalf_of = %v, want local:bob, from the delegation and not the payload", who)
	}

	// A webhook's namespace is the API's own, for a delivery it verified.
	for _, tag := range []string{"halite/hook/github/push", "halite/hook"} {
		code, _, raw = a.runner(t, bob, "event.send", nil, map[string]any{"tag": tag})
		if code == http.StatusOK {
			t.Errorf("an operator through the API put %s on the bus: %s", tag, raw)
		}
	}
	if got := busEvents(t, a.lab, "halite/hook/**"); len(got) > 0 {
		t.Errorf("an operator's event reached the webhook namespace: %v", got)
	}

	// A direct caller names nobody, so a payload's `_on_behalf_of` is
	// removed rather than kept as if the hub had written it. The API's
	// own undelegated call is what a webhook delivery is, so the
	// webhook namespace stays open to it.
	op := a.operator(t, "api")
	for _, tag := range []string{"halite/audit/direct", "halite/hook/manual"} {
		res, err := op.Runner(context.Background(), transport.RunnerRequest{Fun: "event.send",
			Kwarg: map[string]any{"tag": tag, "data": map[string]any{"_on_behalf_of": "local:alice"}}})
		if err != nil || !res.Success {
			t.Fatalf("a direct event.send of %s failed: %v %+v", tag, err, res)
		}
	}
	direct := busEvents(t, a.lab, "halite/audit/direct")
	if len(direct) != 1 {
		t.Fatalf("the bus has %d direct events, want 1", len(direct))
	}
	if who, ok := direct[0].Data["_on_behalf_of"]; ok {
		t.Errorf("a direct caller's payload wrote _on_behalf_of = %v", who)
	}
	if who := direct[0].Data["_principal"]; who != "cert:CN=api" {
		t.Errorf("_principal = %v, want cert:CN=api", who)
	}
}

// **Key actions** name the operator they were taken for, in the event
// that is the audit trail of acceptances and revocations.
func TestKeyActionsThroughTheAPIRecordTheOperator(t *testing.T) {
	a := newAPILab(t, nil)
	bob := a.login(t, "bob")
	for _, id := range []string{"new1.example", "new2.example"} {
		client, key := a.node(t)
		if _, err := client.Enroll(context.Background(), key, id, ""); err == nil {
			t.Fatalf("%s: a first request should be pending", id)
		}
	}
	for _, c := range []struct{ fun, node string }{
		{"key.accept", "new1.example"},
		{"key.revoke", "new1.example"},
		{"key.reject", "new2.example"},
		{"key.delete", "new2.example"},
	} {
		if code, _, raw := a.runner(t, bob, c.fun, nil, map[string]any{"node": c.node}); code != http.StatusOK {
			t.Fatalf("bob could not %s %s through the API: %d %s", c.fun, c.node, code, raw)
		}
	}
	want := map[string]bool{
		"halite/key/new1.example/accept": false,
		"halite/key/new1.example/revoke": false,
		"halite/key/new2.example/reject": false,
		"halite/key/new2.example/delete": false,
	}
	for _, e := range busEvents(t, a.lab, "halite/key/**") {
		if _, ok := want[e.Tag]; !ok {
			continue
		}
		want[e.Tag] = true
		if who := e.Data["principal"]; who != "cert:CN=api" {
			t.Errorf("%s: principal = %v, want cert:CN=api", e.Tag, who)
		}
		if who := e.Data["on_behalf_of"]; who != "local:bob" {
			t.Errorf("%s: on_behalf_of = %v, want local:bob", e.Tag, who)
		}
	}
	for tag, seen := range want {
		if !seen {
			t.Errorf("no %s event", tag)
		}
	}
}
