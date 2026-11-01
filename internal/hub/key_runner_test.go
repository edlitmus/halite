package hub

import (
	"context"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
)

func keyEvents(t *testing.T, l *lab, node string) []string {
	t.Helper()
	events, _, err := l.server.Events.Read(eventbus.Earliest, []string{"halite/key/" + node + "/*"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, e := range events {
		tags = append(tags, e.Tag)
	}
	return tags
}

// `key.revoke` is what an orchestration, a reactor or the API calls to
// withdraw a node. It called the key store directly, so unlike `Server.Revoke`
// (which nothing called) it published no `halite/key/<node>/revoke` event and
// did not end the node's stream: the node stayed connected until the 2-second
// reconcile loop noticed, and a hub embedded without that loop never
// disconnected it. The handbook sells `halite/key/**` as the audit trail of
// "acceptances, revocations". DIVERGENCE 5.511.
func TestKeyRevokeEndsTheStreamAndAnnouncesItself(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	node := l.enrolled(t, "web1.example")
	tr := &tracker{seen: map[string]time.Time{}}
	defer l.connectTracking(t, node, "web1.example", tr, nil)()
	eventually(t, "the node to connect", func() bool {
		_, up := l.server.fleet().Connected()["web1.example"]
		return up
	})

	out, err := l.server.CallRunner(context.Background(), RunnerCall{
		Principal: "cert:CN=ed", Fun: "key.revoke",
		Kwarg: map[string]any{"node": "web1.example", "reason": "decommissioned"},
	})
	if err != nil || !out.Success {
		t.Fatalf("key.revoke failed: %v %+v", err, out)
	}

	// Well inside the reconcile interval, which this lab does not run.
	eventually(t, "the revoked node's stream to end", func() bool {
		_, up := l.server.fleet().Connected()["web1.example"]
		return !up
	})
	eventually(t, "the revoke event", func() bool {
		for _, tag := range keyEvents(t, l, "web1.example") {
			if tag == "halite/key/web1.example/revoke" {
				return true
			}
		}
		return false
	})
}

// Accepting, rejecting and deleting a key are lifecycle actions too.
func TestKeyAcceptRejectAndDeleteAnnounceThemselves(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	for _, id := range []string{"web1.example", "web2.example"} {
		client, key := l.node(t)
		if _, err := client.Enroll(context.Background(), key, id, ""); err == nil {
			t.Fatalf("%s: a first request should be pending", id)
		}
	}
	run := func(fun, node string) {
		t.Helper()
		out, err := l.server.CallRunner(context.Background(), RunnerCall{
			Principal: "cert:CN=ed", Fun: fun, Kwarg: map[string]any{"node": node},
		})
		if err != nil || !out.Success {
			t.Fatalf("%s %s: %v %+v", fun, node, err, out)
		}
	}
	run("key.accept", "web1.example")
	run("key.reject", "web2.example")
	run("key.delete", "web2.example")

	want := map[string][]string{
		"web1.example": {"halite/key/web1.example/accept"},
		"web2.example": {"halite/key/web2.example/reject", "halite/key/web2.example/delete"},
	}
	for node, tags := range want {
		got := keyEvents(t, l, node)
		for _, tag := range tags {
			found := false
			for _, g := range got {
				found = found || g == tag
			}
			if !found {
				t.Errorf("no %s event; the bus has %v", tag, got)
			}
		}
	}
}
