package hub

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// RelayDepth is how many relays a connection may be behind. Zero takes
// SPEC 5.3's default of two.
func (s *Server) relayDepth() int {
	if s.MaxRelayDepth > 0 {
		return s.MaxRelayDepth
	}
	return transport.MaxRelayDepth
}

// acceptRelay records the nodes a relay proxies for.
//
// Authorized as `relay.proxy`, which SPEC 23.1 makes a permission set
// of its own: a relay's certificate covers proxying for its subordinate
// nodes and nothing else, so a compromised relay is a relay rather than
// an operator.
func (s *Server) acceptRelay(relayID string, req transport.SubscribeRequest) error {
	if !s.AcceptRelays {
		// Off by default, and a refusal rather than a silent
		// downgrade to an ordinary node connection: an estate that has
		// not decided to run relays should not acquire one because
		// somebody set a flag on a hub in a branch office.
		return fmt.Errorf("this hub does not accept relays; set `accept_relays: true` on it")
	}
	decision := s.Policy.Authorize(policy.Request{
		Principal: "node:" + relayID,
		Fun:       "relay.proxy",
		Runner:    true,
	})
	s.countDecision(decision)
	if !decision.Allowed {
		return fmt.Errorf("this node is not permitted to relay: %s", decision.Reason)
	}
	if req.Depth >= s.relayDepth() {
		// SPEC 5.3 caps this because unbounded nesting is how syndic
		// estates become undebuggable: a job that does not arrive has
		// to be traced through however many tiers somebody built.
		return fmt.Errorf("this connection is %d relays deep and the limit is %d",
			req.Depth+1, s.relayDepth())
	}

	names, err := s.recordSubordinates(relayID, req.Subordinates)
	if err != nil {
		return err
	}
	s.fleet().Relay(relayID, names)
	s.info("relay connected",
		"relay", relayID, "subordinates", len(names), "depth", req.Depth+1)
	s.emit("halite/relay/"+relayID+"/connected", relayID, map[string]any{
		"subordinates": len(names), "depth": req.Depth + 1,
	})
	return nil
}

// recordSubordinates writes each subordinate's grains into the node
// cache, so targeting upstream works on a relayed node exactly as on a
// directly connected one.
func (s *Server) recordSubordinates(relayID string, subordinates []transport.Subordinate) ([]string, error) {
	names := make([]string, 0, len(subordinates))
	for _, sub := range subordinates {
		if sub.NodeID == "" {
			return nil, errors.New("a relay named a subordinate with no id")
		}
		if sub.NodeID == relayID {
			// A relay claiming itself as its own subordinate would
			// route its jobs to itself for ever.
			return nil, fmt.Errorf("%s named itself as its own subordinate", relayID)
		}
		if strings.ContainsAny(sub.NodeID, "/\\ \t") {
			// The id becomes a cache path and a tag segment.
			return nil, fmt.Errorf("%q is not a usable node id", sub.NodeID)
		}
		names = append(names, sub.NodeID)

		if len(sub.Grains) == 0 {
			continue
		}
		if err := s.nodes().Put(&NodeData{
			NodeID: sub.NodeID, Grains: sub.Grains,
			Version: sub.Version, LastSeen: s.now(),
		}); err != nil && !errors.Is(err, errNoNodeCache) {
			s.warn("could not record a relayed node",
				"relay", relayID, "node_id", sub.NodeID, "error", err.Error())
		}
	}
	return names, nil
}

// relayUpdate is POST /v1/relay: a relay saying its fleet changed.
//
// On a change rather than on a timer, because a hub that learns about a
// new node a minute late is a hub that silently left it out of every
// job in that minute — and reported nothing, because it did not know
// the node existed.
func (s *Server) relayUpdate(w http.ResponseWriter, r *http.Request, relayID string) {
	var req transport.RelayUpdate
	if err := transport.ReadJSON(w, r, transport.MaxGrainsPayload, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	decision := s.Policy.Authorize(policy.Request{
		Principal: "node:" + relayID,
		Fun:       "relay.proxy",
		Runner:    true,
	})
	s.countDecision(decision)
	if !decision.Allowed {
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("this node is not permitted to relay: %s", decision.Reason))
		return
	}

	names, err := s.recordSubordinates(relayID, req.Subordinates)
	if err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	s.fleet().Relay(relayID, names)
	s.info("relay fleet changed",
		"relay", relayID, "subordinates", len(names), "left", len(req.Left))
	transport.WriteJSON(w, http.StatusOK, map[string]any{"subordinates": len(names)})
}

// relayReturn reports whether a return arriving on a relay's
// certificate is for a node that relay proxies for.
//
// A relay may post a return for its own subordinates and for nobody
// else. Without this check a relay could file a return for any node in
// the estate, which is a job that looks like it succeeded.
func (s *Server) relayMayReturn(relayID, nodeID string) bool {
	if relayID == nodeID {
		return true
	}
	via, ok := s.fleet().RelayFor(nodeID)
	return ok && via == relayID
}

// targetableNodes is every node a job on this hub may match: the ones
// whose keys it accepted, and the subordinates of its relays.
//
// A relayed node has no key here and never will — the relay issued it,
// which is the whole of the arrangement in SPEC 5.3. Resolving against
// the keystore alone left every relayed node untargetable while
// Connected cheerfully reported it as up, so a job aimed at one matched
// nothing and said so as if the node were absent.
func (s *Server) targetableNodes() ([]string, error) {
	records, err := s.Authority.Store.List()
	if err != nil {
		return nil, err
	}
	now := s.now()
	seen := make(map[string]bool, len(records))
	var out []string
	for _, rec := range records {
		if rec.Status(now) != keystore.Accepted || seen[rec.NodeID] {
			continue
		}
		seen[rec.NodeID] = true
		out = append(out, rec.NodeID)
	}
	for node := range s.fleet().Relayed() {
		if seen[node] {
			continue
		}
		seen[node] = true
		out = append(out, node)
	}
	sort.Strings(out)
	return out, nil
}
