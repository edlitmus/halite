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
		if err := s.nodes().Put(&NodeData{
			NodeID:   nodeID,
			Grains:   req.Grains,
			Version:  req.Version,
			LastSeen: now,
		}); err != nil && !errors.Is(err, errNoNodeCache) {
			s.warn("could not record what a node reported", "node_id", nodeID, "error", err.Error())
		}
	}

	st := s.fleet().attach(nodeID, now)
	defer s.fleet().detach(st)

	s.info("node connected", "node_id", nodeID, "version", req.Version, "grains_bytes", len(req.Grains))
	s.emit(tagNodeStart(nodeID), nodeID, map[string]any{"version": req.Version})
	s.emit("halite/presence/change", nodeID, map[string]any{
		"connected": len(s.fleet().Connected()), "joined": nodeID,
	})
	defer func() {
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
			return
		case msg := <-st.out:
			if err := write(msg); err != nil {
				return
			}
			if msg.Final {
				return
			}
		case <-ticker.C:
			seq++
			// A ping is how a node tells a quiet hub from a dead one,
			// and how the hub learns the connection is gone: a write
			// to a vanished peer is what fails.
			if err := write(transport.Message{T: transport.MsgPing, Seq: seq}); err != nil {
				return
			}
		}
	}
}

func (s *Server) fleet() *Fleet {
	s.fleetOnce.Do(func() { s.Fleet = newFleet() })
	return s.Fleet
}

// Revoke withdraws a node's enrollment and acts on it at once: the
// serial reaches the handshake denylist, and the node is told on its
// own stream that it should stop, per SPEC 7.4.
func (s *Server) Revoke(nodeID, reason string) error {
	if _, err := s.Authority.Revoke(nodeID, reason); err != nil {
		return err
	}
	s.emit(tagKey(nodeID, "revoke"), nodeID, map[string]any{"reason": reason})
	s.fleet().Disconnect(nodeID, reason)
	s.info("enrollment revoked", "node_id", nodeID, "reason", reason)
	return nil
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
	revoker := s.Authority.Revoker
	for _, rec := range records {
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
