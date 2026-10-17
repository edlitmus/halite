package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/transport"
)

func relayBoundServer(t *testing.T, globs map[string][]string) *Server {
	t.Helper()
	store, err := keystore.Open(t.TempDir() + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := OpenNodeCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		AcceptRelays: true, Policy: allowEverything(t), RelaySubordinates: globs,
		Authority: &keystore.Authority{Store: store}, Nodes: nodes,
	}
}

func claimed(s *Server, nodeID string) bool {
	_, ok := s.fleet().RelayFor(nodeID)
	return ok
}

// **A relay claims only what relay_subordinates gives it** (issue 321,
// M-15). On main a relay with `relay.proxy` could name any node id not
// connected at that moment, and the hub routed that node's jobs to it.
func TestARelayClaimsOnlyItsOwnSubordinates(t *testing.T) {
	s := relayBoundServer(t, map[string][]string{"relay1.example": {"web*.dmz"}})
	s.fleet().attach("relay1.example", nowForTest())
	if err := s.acceptRelay("relay1.example", transport.SubscribeRequest{
		Relay: true,
		Subordinates: []transport.Subordinate{
			{NodeID: "web1.dmz"}, {NodeID: "db1.prod"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if !claimed(s, "web1.dmz") {
		t.Error("a subordinate inside the relay's globs was not claimed")
	}
	if claimed(s, "db1.prod") {
		t.Error("a node outside the relay's globs was claimed")
	}

	// A relay with no entry claims nothing.
	other := relayBoundServer(t, nil)
	other.fleet().attach("relay1.example", nowForTest())
	if err := other.acceptRelay("relay1.example", transport.SubscribeRequest{
		Relay: true, Subordinates: []transport.Subordinate{{NodeID: "web1.dmz"}},
	}); err != nil {
		t.Fatal(err)
	}
	if claimed(other, "web1.dmz") {
		t.Error("a relay with no relay_subordinates entry claimed a node")
	}
}

// **No relay claims a node enrolled here directly**, connected or not,
// and its grains are not overwritten. DIVERGENCE 1.9 said a relay could
// not claim a directly connected node; that held at the instant of the
// claim, and a node merely offline could be taken, its cached grains --
// which targeting reads -- replaced with the relay's.
func TestARelayCannotClaimANodeEnrolledHere(t *testing.T) {
	s := relayBoundServer(t, map[string][]string{"relay1.example": {"*"}})
	if err := s.Authority.Store.Put(&keystore.Record{
		NodeID: "db1.prod", State: keystore.Accepted, Fingerprint: "aa",
		NotAfter: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Nodes.Put(&NodeData{NodeID: "db1.prod", Grains: json.RawMessage(`{"role":"database"}`)}); err != nil {
		t.Fatal(err)
	}
	s.fleet().attach("relay1.example", nowForTest())
	if err := s.acceptRelay("relay1.example", transport.SubscribeRequest{
		Relay: true,
		Subordinates: []transport.Subordinate{
			{NodeID: "db1.prod", Grains: json.RawMessage(`{"role":"anything-the-relay-likes"}`)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if claimed(s, "db1.prod") {
		t.Error("a relay claimed a node enrolled with this hub")
	}
	got, err := s.Nodes.Get("db1.prod")
	if err != nil {
		t.Fatal(err)
	}
	var grains map[string]any
	if err := json.Unmarshal(got.Grains, &grains); err != nil || grains["role"] != "database" {
		t.Errorf("the enrolled node's grains became %s (%v)", got.Grains, err)
	}
}
