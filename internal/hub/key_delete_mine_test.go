package hub

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/edlitmus/halite/internal/eventbus"
)

// Deleting a key forgets what the node published and reported.
//
// The mine is served to other nodes, so a deleted host stayed in the backend
// list a load balancer's state builds from `mine.get` until somebody knew to
// run `cache.clear_mine`. DIVERGENCE 5.521.
func TestKeyDeleteClearsTheNodesMineAndGrains(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t).withMine(t)
	l.enrolled(t, "web1.example")
	l.enrolled(t, "web2.example")
	for _, id := range []string{"web1.example", "web2.example"} {
		if err := l.server.mine().Put(&MineData{NodeID: id, Functions: map[string]*MineEntry{
			"addr": {Data: json.RawMessage(`"10.0.0.1"`)},
		}}); err != nil {
			t.Fatal(err)
		}
		if err := l.server.nodes().Put(&NodeData{NodeID: id, Grains: json.RawMessage(`{"os":"x"}`)}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := l.server.CallRunner(context.Background(), RunnerCall{
		Principal: "cert:CN=ed", Fun: "key.delete", Kwarg: map[string]any{"node": "web1.example"},
	})
	if err != nil || !out.Success {
		t.Fatalf("key.delete failed: %v %+v", err, out)
	}

	if _, err := l.server.mine().Get("web1.example"); !errors.Is(err, ErrNoMineData) {
		t.Errorf("the deleted node's mine is still served: %v", err)
	}
	if _, err := l.server.nodes().Get("web1.example"); !errors.Is(err, ErrUnknownNode) {
		t.Errorf("the deleted node's grains are still cached: %v", err)
	}
	if _, err := l.server.mine().Get("web2.example"); err != nil {
		t.Errorf("another node's mine was touched: %v", err)
	}
	if _, err := l.server.nodes().Get("web2.example"); err != nil {
		t.Errorf("another node's grains were touched: %v", err)
	}
	events, _, err := l.server.Events.Read(eventbus.Earliest, []string{"halite/key/web1.example/delete"}, 5)
	if err != nil || len(events) != 1 {
		t.Errorf("the delete event: %v %d", err, len(events))
	}
}

// A record deleted by another process, `halite-hub keys delete` at a
// terminal, never reaches the runner; the reconcile loop is what sees it, and
// forgets the node there, unless the name has enrolled again.
func TestReconcileClearsTheMineOfANodeDeletedElsewhere(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t).withMine(t)
	l.enrolled(t, "web1.example")
	l.enrolled(t, "web2.example")
	for _, id := range []string{"web1.example", "web2.example"} {
		if err := l.server.mine().Put(&MineData{NodeID: id, Functions: map[string]*MineEntry{
			"addr": {Data: json.RawMessage(`"10.0.0.1"`)},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	// The hub's own store, as the other process would have it.
	if _, err := l.server.Authority.Delete("web1.example"); err != nil {
		t.Fatal(err)
	}
	if err := l.server.reconcileOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.server.mine().Get("web1.example"); !errors.Is(err, ErrNoMineData) {
		t.Errorf("a node deleted elsewhere still has its mine: %v", err)
	}
	if _, err := l.server.mine().Get("web2.example"); err != nil {
		t.Errorf("a node nobody deleted lost its mine: %v", err)
	}
}
