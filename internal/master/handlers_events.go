package master

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/event"
	"github.com/edlitmus/halite/internal/transport"
)

// eventsHandler routes /v1/events: operators stream it, agents post to it.
// The two share a path because they are two halves of one bus.
func (s *Server) eventsHandler() http.Handler {
	return s.authorized(func(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
		if r.Method == http.MethodPost {
			s.handleAgentEvent(w, r, peer)
			return
		}
		if peer.Role != ca.RoleAdmin {
			writeError(w, http.StatusForbidden, "streaming events requires an operator certificate")
			return
		}
		s.handleEventStream(w, r)
	}, func(ca.Role) bool { return true })
}

// handleAgentEvent accepts an event an agent raised — a beacon firing, say.
// The source is taken from the certificate, so an agent can only ever speak
// for itself.
func (s *Server) handleAgentEvent(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
	var ev transport.Event
	if err := decode(r, &ev); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if ev.Tag == "" {
		writeError(w, http.StatusBadRequest, "an event needs a tag")
		return
	}
	if err := agentMayRaise(peer.ID, ev.Tag); err != nil {
		s.log.Printf("refused event %q from %q: %v", ev.Tag, peer.ID, err)
		writeError(w, http.StatusForbidden, "%v", err)
		return
	}
	// Like Source, Time is the server's to say: the bus stamps it on
	// publish. A time copied from the body would let an agent backdate or
	// postdate its events in the history every operator reads.
	s.bus.Publish(event.Event{
		Tag:    ev.Tag,
		Source: peer.ID,
		Data:   ev.Data,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// agentMayRaise constrains the tags an agent can put on the bus. The
// source of an event is already taken from the certificate, but reactor
// rules match on the *tag* — so without this, web1 could raise
// `halite/beacon/db1/service` and fire a rule written for db1.
//
// Agents may raise `halite/beacon/<their-own-id>/<name>` and
// `halite/schedule/<their-own-id>/<name>`. Anything else under `halite/`
// belongs to the control plane.
func agentMayRaise(id, tag string) error {
	segments := strings.Split(tag, "/")
	if len(segments) != 4 || segments[0] != "halite" || !agentNamespaces[segments[1]] {
		return fmt.Errorf("agents may only raise halite/{beacon,schedule}/%s/<name> events", id)
	}
	if segments[2] != id {
		return fmt.Errorf("cannot raise an event for %q", segments[2])
	}
	if segments[3] == "" {
		return fmt.Errorf("%s events need a name", segments[1])
	}
	return nil
}

// agentNamespaces are the tag namespaces an agent speaks in: what it
// watched, and what it ran on its own clock.
var agentNamespaces = map[string]bool{"beacon": true, "schedule": true}

// handleEventStream writes newline-delimited JSON events until the client
// goes away. Each event is flushed on its own, so a tail is live rather
// than buffered — this is the long-lived stream ADR-5 reserved for the
// event bus.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "this server cannot stream")
		return
	}
	pattern := r.URL.Query().Get("tag")
	if pattern == "" {
		pattern = "**"
	}

	// Subscribe before replaying history, so an event raised between the two
	// is delivered late rather than lost. The cost is a possible duplicate,
	// which a tail can live with and a reactor deduplicates by id.
	events, cancel := s.bus.Subscribe(pattern)
	defer cancel()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	encoder := json.NewEncoder(w)
	if limit, wanted := historyLimit(r); wanted {
		for _, ev := range s.bus.History(pattern, limit) {
			if err := encoder.Encode(wireEvent(ev)); err != nil {
				return
			}
		}
	}
	flusher.Flush()

	// A keepalive keeps intermediaries from reaping an idle stream, and
	// gives the write a chance to fail so a vanished client is noticed.
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case ev, open := <-events:
			if !open {
				return
			}
			if err := encoder.Encode(wireEvent(ev)); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// historyLimit reads ?history=N. A tail starts from now unless it asks for
// the backlog; history=0 means everything the bus still holds.
func historyLimit(r *http.Request) (limit int, wanted bool) {
	raw := r.URL.Query().Get("history")
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func wireEvent(ev event.Event) transport.Event {
	return transport.Event{
		ID:     ev.ID,
		Tag:    ev.Tag,
		Time:   ev.Time,
		Source: ev.Source,
		Data:   ev.Data,
	}
}
