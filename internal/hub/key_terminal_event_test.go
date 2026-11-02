package hub

import (
	"context"
	"testing"

	"github.com/edlitmus/halite/internal/eventbus"
)

func keyEventsFull(t *testing.T, l *lab, node string) []eventbus.Event {
	t.Helper()
	events, _, err := l.server.Events.Read(eventbus.Earliest, []string{"halite/key/" + node + "/*"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// A key decision made at a terminal is announced.
//
// `halite-hub keys accept|reject|revoke|delete` is another process. It changed
// the store and published nothing, so the bus held no record of a key being
// revoked that way, and a reaction on `halite/key/*/revoke` never fired for it.
// The reconcile loop reads the store, so it announces what it finds changed.
// DIVERGENCE 5.536.
func TestAKeyChangedAtATerminalIsAnnounced(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	for _, id := range []string{"web1.example", "web2.example"} {
		client, key := l.node(t)
		if _, err := client.Enroll(context.Background(), key, id, ""); err == nil {
			t.Fatalf("%s: a first request should be pending", id)
		}
	}
	// The first pass only learns what is there.
	if err := l.server.reconcileOnce(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"web1.example", "web2.example"} {
		if got := keyEventsFull(t, l, id); len(got) != 0 {
			t.Fatalf("history was announced for %s: %d events", id, len(got))
		}
	}

	// As `halite-hub keys` would: straight to the store, never the runner.
	steps := []struct {
		node, action string
		do           func() error
	}{
		{"web1.example", "accept", func() error { _, err := l.server.Authority.Accept("web1.example"); return err }},
		{"web2.example", "reject", func() error { _, err := l.server.Authority.Reject("web2.example", "not ours"); return err }},
		{"web1.example", "revoke", func() error { _, err := l.server.Authority.Revoke("web1.example", "decommissioned"); return err }},
		{"web1.example", "delete", func() error { _, err := l.server.Authority.Delete("web1.example"); return err }},
	}
	seen := map[string]int{}
	for _, step := range steps {
		if err := step.do(); err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
		if err := l.server.reconcileOnce(); err != nil {
			t.Fatal(err)
		}
		got := keyEventsFull(t, l, step.node)
		n := seen[step.node]
		if len(got) != n+1 || got[n].Tag != "halite/key/"+step.node+"/"+step.action {
			t.Fatalf("after %s of %s the key events are %v", step.action, step.node, got)
		}
		if got[n].Data["via"] != "terminal" {
			t.Errorf("%s: the event does not say it came from outside: %v", step.action, got[n].Data)
		}
		seen[step.node]++
	}
	// And once only: a loop that finds nothing new says nothing.
	if err := l.server.reconcileOnce(); err != nil {
		t.Fatal(err)
	}
	if got := keyEventsFull(t, l, "web1.example"); len(got) != 3 {
		t.Errorf("a quiet pass announced again: %d events", len(got))
	}
}

// A change this process made and announced is not announced a second time when
// the loop reads it back.
func TestAKeyChangedByTheRunnerIsNotAnnouncedTwice(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.enrolled(t, "web1.example")
	if err := l.server.reconcileOnce(); err != nil {
		t.Fatal(err)
	}
	out, err := l.server.CallRunner(context.Background(), RunnerCall{
		Principal: "cert:CN=ed", Fun: "key.revoke", Kwarg: map[string]any{"node": "web1.example"},
	})
	if err != nil || !out.Success {
		t.Fatalf("key.revoke: %v %+v", err, out)
	}
	for i := 0; i < 2; i++ {
		if err := l.server.reconcileOnce(); err != nil {
			t.Fatal(err)
		}
	}
	got := keyEventsFull(t, l, "web1.example")
	if len(got) != 1 || got[0].Data["principal"] != "cert:CN=ed" {
		t.Errorf("the runner's revoke was announced %d times: %v", len(got), got)
	}
}
