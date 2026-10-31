package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

func postRelayUpdate(t *testing.T, s *Server, relayID string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(transport.RelayUpdate{
		Subordinates: []transport.Subordinate{{NodeID: "web1.example"}},
	})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", transport.PathRelay, bytes.NewReader(body))
	s.relayUpdate(rec, r, relayID)
	return rec
}

// `accept_relays` is off by default, and the subscribe path refuses a relay
// outright rather than downgrading it, so that an estate that has not decided
// to run relays does not acquire one. POST /v1/relay did not look at it: a
// node the policy let `relay.proxy` registered subordinates, and the hub then
// routed those nodes' jobs to it, on a hub that had said it takes no relays.
func TestAnUpdateFromARelayIsRefusedUnlessTheHubAcceptsThem(t *testing.T) {
	s := &Server{
		Policy:            allowEverything(t),
		RelaySubordinates: map[string][]string{"relay1.example": {"web*"}},
	}
	rec := postRelayUpdate(t, s, "relay1.example")
	if rec.Code != http.StatusForbidden {
		t.Errorf("an update to a hub that accepts no relays answered %d, not 403", rec.Code)
	}
	if _, ok := s.fleet().RelayFor("web1.example"); ok {
		t.Error("a hub that accepts no relays routed a node through one")
	}

	s.AcceptRelays = true
	if rec := postRelayUpdate(t, s, "relay1.example"); rec.Code != http.StatusOK {
		t.Errorf("an update to a hub that accepts relays answered %d: %s", rec.Code, rec.Body)
	}
}
