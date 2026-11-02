package hub

import (
	"testing"
	"time"
)

// A relay's update that arrives after its stream has gone leaves no routes.
//
// `POST /v1/relay` is accepted whether or not the relay has a stream open,
// deliberately: the relay announces concurrently with opening one, so the
// update may arrive first. But the routes it recorded outlived a relay that was
// not there. `dropRelay` runs when the stream ends, and an update after that
// recreated what it had just removed, so `targetableNodes` listed nodes no job
// could reach and `Connected` hid them, until the relay next came and went.
// DIVERGENCE 5.533.
func TestAnUpdateFromARelayWithNoStreamLeavesNoRoutes(t *testing.T) {
	f := newFleet()
	now := time.Now()
	f.now = func() time.Time { return now }

	st := f.attach("relay1.example", now)
	f.Relay("relay1.example", []string{"web1.example"})
	if _, ok := f.RelayFor("web1.example"); !ok {
		t.Fatal("a relay with a stream could not route")
	}
	f.detach(st)
	f.dropRelay("relay1.example")
	if len(f.Relayed()) != 0 {
		t.Fatal("the relay's routes outlived its stream")
	}

	// The late update.
	f.Relay("relay1.example", []string{"web1.example", "web2.example"})
	if got := f.Relayed(); len(got) != 0 {
		t.Errorf("an update from a relay that is not here left routes: %v", got)
	}

	// And it does not come back when the relay does, if it is old: the relay
	// announces again when it connects, and what it said before is not news.
	now = now.Add(2 * relayAnnounceGrace)
	f.attach("relay1.example", now)
	if got := f.Relayed(); len(got) != 0 {
		t.Errorf("a stale announcement was applied when the relay reconnected: %v", got)
	}
}

// The order the protocol allows still works: the announcement first, the
// stream a moment later.
func TestAnAnnouncementThatArrivesBeforeTheStreamIsApplied(t *testing.T) {
	f := newFleet()
	now := time.Now()
	f.now = func() time.Time { return now }

	f.Relay("relay1.example", []string{"web1.example"})
	if len(f.Relayed()) != 0 {
		t.Error("routes were live with no stream behind them")
	}
	now = now.Add(time.Second)
	f.attach("relay1.example", now)
	if via, ok := f.RelayFor("web1.example"); !ok || via != "relay1.example" {
		t.Errorf("the early announcement was lost: %v %v", via, ok)
	}
	if _, ok := f.Connected()["web1.example"]; !ok {
		t.Error("a node behind a relay that announced first is not connected")
	}
}
