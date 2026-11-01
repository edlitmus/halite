package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/transport"
)

// subscribe is the long-lived stream of SPEC 6.2: the hub writes NDJSON
// to the node until one of them goes away.
func (s *Server) subscribe(w http.ResponseWriter, r *http.Request, nodeID string) {
	var req transport.SubscribeRequest
	if err := transport.ReadJSON(w, r, transport.MaxGrainsPayload, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	// The body may say who it is; the certificate decides. A
	// disagreement is not resolved in either direction, because a node
	// that believes it is called something else is a real problem and
	// silently overruling it hides it.
	if req.NodeID != "" && req.NodeID != nodeID {
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("the certificate says %s and the request says %s", nodeID, req.NodeID))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal,
			errors.New("this connection cannot stream"))
		return
	}

	now := s.now()
	// The grains a node reports are what targeting on a grain reads,
	// and they are recorded before the stream opens: a job dispatched
	// in the same second as a node connects should not miss it.
	if len(req.Grains) > 0 || req.Version != "" {
		// A report that carries a version and no grains does not erase the
		// ones the hub holds. The node sends nil when it cannot encode its
		// grains (`grainsJSON` logs and returns nothing), and this wrote
		// that over the cache: every target on a grain then matched nothing
		// for that node until its next refresh, which is the silent miss
		// SPEC 8.3 says must be annotated and not hidden. The grains it did
		// report earlier keep their own age, so `grain_stale_after` still
		// says how old they are: the time recorded is when they were
		// reported, not when the node last connected. DIVERGENCE 5.523.
		grains, seen := req.Grains, now
		if len(grains) == 0 {
			if old, err := s.nodes().Get(nodeID); err == nil {
				grains, seen = old.Grains, old.LastSeen
			}
		}
		if err := s.nodes().Put(&NodeData{
			NodeID:   nodeID,
			Grains:   grains,
			Version:  req.Version,
			LastSeen: seen,
		}); err != nil && !errors.Is(err, errNoNodeCache) {
			s.warn("could not record what a node reported", "node_id", nodeID, "error", err.Error())
		}
	}

	// A relay presents itself upstream as a single client and names the
	// nodes behind it. SPEC 5.3.
	if req.Relay {
		if err := s.acceptRelay(nodeID, req); err != nil {
			transport.WriteError(w, http.StatusForbidden, transport.CodeRefused, err)
			return
		}
	}

	st := s.fleet().attach(nodeID, now)

	s.m().nodeConnects.Inc()
	// Why a stream ended is the useful half: a node that went away and
	// one the hub cut off are different incidents, and a stream that
	// ended because the write failed is a third.
	reason := "client_gone"

	s.info("node connected", "node_id", nodeID, "version", req.Version, "grains_bytes", len(req.Grains))
	s.emit(tagNodeStart(nodeID), nodeID, map[string]any{"version": req.Version})
	s.emit("halite/presence/change", nodeID, map[string]any{
		"connected": len(s.fleet().Connected()), "joined": nodeID,
	})
	defer func() {
		// Detached first, so that the count below is the fleet without
		// this node; it was taken before, and a node leaving an estate of
		// one reported one connected.
		current := s.fleet().detach(st)
		s.m().nodeDisconnects.With(reason).Inc()
		if !current {
			// A second connection from this identity replaced this one,
			// which is how a reconnecting node, or relay, arrives while the
			// hub has not yet noticed the old peer is gone. This handler
			// returning is then the old stream ending and nothing else: the
			// node has not stopped, and the routes a relay registered are
			// the new connection's, which `dropRelay` removes by relay name
			// and cannot tell from this one's. Both used to run, so a relay
			// that reconnected had its subordinates forgotten until it next
			// sent an update, and a reactor on `node/<id>/stop` was told
			// about a node that was connected. DIVERGENCE 5.506.
			s.info("a node's earlier connection ended; it has reconnected", "node_id", nodeID)
			return
		}
		if req.Relay {
			s.fleet().dropRelay(nodeID)
		}
		s.info("node disconnected", "node_id", nodeID)
		s.emit(tagNodeStop(nodeID), nodeID, nil)
		s.emit("halite/presence/change", nodeID, map[string]any{
			"connected": len(s.fleet().Connected()), "left": nodeID,
		})
	}()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Anything this node missed while it was away, per SPEC 9.5's
	// `queue` policy. After the stream is open, so a delivery has
	// somewhere to go.
	s.goBackground(func() { s.deliverQueued(nodeID) })

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	write := func(msg transport.Message) error {
		if err := enc.Encode(msg); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	ticker := time.NewTicker(s.pingInterval())
	defer ticker.Stop()
	var seq int64

	for {
		select {
		case <-r.Context().Done():
			return
		case <-st.done:
			reason = "hub_closed"
			return
		case msg := <-st.out:
			if err := write(msg); err != nil {
				reason = "write_failed"
				return
			}
			if msg.Final {
				reason = "final_message"
				return
			}
		case <-ticker.C:
			seq++
			// A ping is how a node tells a quiet hub from a dead one,
			// and how the hub learns the connection is gone: a write
			// to a vanished peer is what fails.
			if err := write(transport.Message{T: transport.MsgPing, Seq: seq}); err != nil {
				reason = "ping_failed"
				return
			}
		}
	}
}

func (s *Server) fleet() *Fleet {
	// A caller that set one keeps it. The first cut replaced it
	// unconditionally, so the field's own documentation — "a caller may
	// set one to share it" — was false the moment anything touched the
	// fleet, and a relay holding a reference to the shared one watched
	// a fleet nothing connected to.
	s.fleetOnce.Do(func() {
		if s.Fleet == nil {
			s.Fleet = newFleet()
		}
	})
	return s.Fleet
}

// Revoke withdraws a node's enrollment and acts on it at once: the
// serial reaches the handshake denylist, and the node is told on its
// own stream that it should stop, per SPEC 7.4.
func (s *Server) Revoke(nodeID, reason string) error {
	_, err := s.revokeBy("", nodeID, reason)
	return err
}

// revokeBy is Revoke for a caller who is somebody: the `key.revoke` runner
// has a principal, and the event it publishes says who. It answers with the
// record, which the runner returns.
//
// The runner used to call the key store itself. That withdrew the
// enrollment and published nothing, and left the node's stream open until
// the reconcile loop's next pass (two seconds, and never on a hub run
// without it); `Revoke` above, which does both, had no caller. DIVERGENCE
// 5.511.
func (s *Server) revokeBy(principal, nodeID, reason string) (*keystore.Record, error) {
	rec, err := s.Authority.Revoke(nodeID, reason)
	if err != nil {
		return nil, err
	}
	s.announceKey(principal, nodeID, "revoke", map[string]any{"reason": reason})
	s.fleet().Disconnect(nodeID, reason)
	s.info("enrollment revoked", "node_id", nodeID, "reason", reason, "principal", principal)
	return rec, nil
}

// announceKey publishes a key lifecycle action as `halite/key/<node>/<action>`,
// naming the principal when there is one. The handbook describes this
// namespace as the audit trail of acceptances and revocations; until the
// key runners called this, only a renewal and the unused `Revoke` ever wrote
// to it.
func (s *Server) announceKey(principal, nodeID, action string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if principal != "" {
		data["principal"] = principal
	}
	s.emit(tagKey(nodeID, action), nodeID, data)
}

// Reconcile keeps the running hub's handshake denylist in step with the
// key store on disk.
//
// `halite-hub keys revoke` is a separate process from `halite-hub
// serve`, and the denylist SPEC 7.4 asks for lives in the serving
// process's memory. Rather than an IPC channel for one message, the
// store is the channel: the record on disk is the decision, and the
// server follows it within one interval. When both happen in the same
// process -- an operator API, or a test -- Revoke above is immediate
// and this loop finds nothing to do.
func (s *Server) Reconcile(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.reconcileOnce(); err != nil {
				s.warn("reading the key store", "error", err.Error())
			}
		}
	}
}

func (s *Server) reconcileOnce() error {
	records, err := s.Authority.Store.List()
	if err != nil {
		return err
	}
	deleted, err := s.Authority.Store.ListDeleted()
	if err != nil {
		return err
	}
	revoker := s.Authority.Revoker
	now := s.now()
	// A certificate whose record was deleted, by whichever process. The
	// node is told only while no accepted record has taken its name: one
	// that has enrolled again is holding a different certificate, and its
	// stream is not the one to end.
	accepted := map[string]bool{}
	for _, rec := range records {
		if rec.State == keystore.Accepted {
			accepted[rec.NodeID] = true
		}
	}
	for _, d := range deleted {
		if !d.Live(now) {
			continue
		}
		if revoker != nil {
			revoker.Revoke(d.Serial, d.Reason)
		}
		// What the hub kept for the node goes too, whichever process
		// deleted the record: `halite-hub keys delete` at a terminal never
		// reaches the runner. Not for a name that has enrolled again, whose
		// record is somebody else's now. DIVERGENCE 5.521.
		if !accepted[d.NodeID] {
			s.forgetNode(d.NodeID)
		}
		if !accepted[d.NodeID] && s.fleet().Disconnect(d.NodeID, d.Reason) {
			s.info("a node's record was deleted elsewhere; the node has been told",
				"node_id", d.NodeID, "reason", d.Reason)
		}
	}
	for _, rec := range records {
		// A serial a renewal replaced, whichever process renewed it.
		// The one that did denylisted it in its own memory, which is
		// not this hub's when the renewal came from the command line
		// or from before a restart (issue 321, M-05).
		if revoker != nil {
			for _, sup := range rec.Superseded {
				if sup.Live(now) {
					revoker.Revoke(sup.Serial, keystore.ReasonSuperseded)
				}
			}
		}
		if rec.Serial == "" {
			continue
		}
		switch rec.State {
		case keystore.Revoked:
			if revoker != nil {
				revoker.Revoke(rec.Serial, reasonOr(rec.Reason))
			}
			if s.fleet().Disconnect(rec.NodeID, reasonOr(rec.Reason)) {
				s.info("enrollment revoked elsewhere; the node has been told",
					"node_id", rec.NodeID, "reason", reasonOr(rec.Reason))
			}
		case keystore.Accepted:
			// A record that was revoked and then accepted again -- a
			// rebuilt machine, say -- must come off the denylist, or
			// it enrols successfully and then cannot connect.
			if revoker != nil {
				revoker.Allow(rec.Serial)
			}
		}
	}
	return nil
}

func reasonOr(reason string) string {
	if reason == "" {
		return "enrollment revoked"
	}
	return reason
}

// forgetNode drops what the hub keeps for a node whose key record has been
// deleted: the mine it published and the grains it reported.
//
// Deleting the record left both. The mine is served to other nodes, so a
// decommissioned host stayed in the backend list a load balancer's state
// built from `mine.get`, until somebody knew to run `cache.clear_mine`; and a
// node cache entry for a name nothing can connect as is clutter that a grain
// survey reads. Revocation is not deletion and does not call this: a revoked
// record can be accepted again, and the node has not been removed from the
// estate. A name that enrols again publishes and reports afresh. DIVERGENCE
// 5.521.
func (s *Server) forgetNode(nodeID string) {
	if err := s.mine().Delete(nodeID, ""); err != nil && !errors.Is(err, errNoMineStore) {
		s.warn("could not clear a deleted node's mine", "node_id", nodeID, "error", err.Error())
	}
	if err := s.nodes().Delete(nodeID); err != nil && !errors.Is(err, errNoNodeCache) {
		s.warn("could not clear a deleted node's cached grains", "node_id", nodeID, "error", err.Error())
	}
}
