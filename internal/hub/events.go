package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// emit puts an event on the bus.
//
// A hub with no bus drops it, deliberately and quietly: the bus is
// optional configuration, and a hub that failed a job because it could
// not record that the job started would be worse than one that does not
// record it.
func (s *Server) emit(tag string, node string, data map[string]any) {
	s.emitCorrelated(tag, node, "", data)
}

// emitCorrelated is the same with the causality chain named.
//
// A chain is how "what did this cause" gets an answer, and how a
// reactor tells a loop from a busy estate: an event, the job a reaction
// dispatched for it, and the events that job produced all carry the
// same identifier. SPEC 17.1 and 16.3.
func (s *Server) emitCorrelated(tag, node, correlation string, data map[string]any) {
	if s.Events == nil {
		return
	}
	e := &eventbus.Event{Tag: tag, Node: node, Stamp: s.now(), Data: data, Correlation: correlation}
	if _, err := s.Events.Append(e); err != nil {
		s.warn("could not record an event", "tag", tag, "error", err.Error())
		return
	}
	s.emitSaltCompat(e)
}

// emitSaltCompat writes the `salt/...` spelling as well, for SPEC
// 17.1's `event_tag_compat`: a transition period where an existing
// consumer cannot be changed at the same time as the estate.
func (s *Server) emitSaltCompat(e *eventbus.Event) {
	if !s.EventTagCompat {
		return
	}
	salt := eventbus.SaltTag(e.Tag)
	if salt == "" || salt == e.Tag {
		return
	}
	copied := *e
	copied.Tag = salt
	if _, err := s.Events.Append(&copied); err != nil {
		s.warn("could not record a compatibility event", "tag", salt, "error", err.Error())
	}
}

// The tags of SPEC 17.1 this build fires.
func tagJobNew(jid string) string         { return "halite/job/" + jid + "/new" }
func tagJobRet(jid, node string) string   { return "halite/job/" + jid + "/ret/" + node }
func tagNodeStart(node string) string     { return "halite/node/" + node + "/start" }
func tagNodeStop(node string) string      { return "halite/node/" + node + "/stop" }
func tagEnroll(node, state string) string { return "halite/node/" + node + "/enroll/" + state }
func tagKey(node, action string) string   { return "halite/key/" + node + "/" + action }
func tagRunNew(jid string) string         { return "halite/run/" + jid + "/new" }
func tagRunRet(jid string) string         { return "halite/run/" + jid + "/ret" }
func tagOrchNew(jid string) string        { return "halite/run/" + jid + "/orch/new" }
func tagOrchRet(jid string) string        { return "halite/run/" + jid + "/orch/ret" }
func tagOrchStep(jid, step string) string { return "halite/run/" + jid + "/orch/step/" + step }
func tagState(jid, node, result string) string {
	return "halite/state/" + jid + "/" + node + "/" + result
}

// events is POST /v1/event: a node putting something on the hub's bus,
// which is `event.send` on the node.
//
// The tag is namespaced under the node so that a node cannot forge an
// event that looks like the hub's own. SPEC 18.3 makes the point about
// the reactor: a node that can fire the right event can otherwise cause
// fleet-wide execution, and Salt's reactor runs with full master // lexicon:allow
// privilege.
func (s *Server) events(w http.ResponseWriter, r *http.Request, nodeID string) {
	var req transport.EventRequest
	if err := transport.ReadJSON(w, r, transport.MaxGrainsPayload, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if s.Events == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub keeps no event bus"))
		return
	}
	tag, err := nodeEventTag(nodeID, req.Tag)
	if err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	var data map[string]any
	if len(req.Data) > 0 {
		// UseNumber and then the model, for the reason SPEC 6.4 gives:
		// the standard decoder turns every number into a float64, and
		// a node reporting a 64-bit identifier would have it changed
		// on the way onto the log.
		dec := json.NewDecoder(bytes.NewReader(req.Data))
		dec.UseNumber()
		if err := dec.Decode(&data); err != nil {
			transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed,
				fmt.Errorf("the event data is not readable: %w", err))
			return
		}
		for k, v := range data {
			data[k] = value.FromJSON(v)
		}
	}
	offset, err := s.Events.Append(&eventbus.Event{
		Tag:         tag,
		Node:        nodeID,
		Stamp:       s.now(),
		Correlation: req.Correlation,
		Data:        data,
	})
	if err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	s.info("event from a node", "node_id", nodeID, "tag", tag)
	transport.WriteJSON(w, http.StatusAccepted, transport.EventResponse{Tag: tag, Offset: offset})
}

// nodeEventTag namespaces what a node sends.
//
// A node may write under `halite/node/<its own id>/...` and nowhere
// else. Anything it asks for is placed there: a node that sends
// `deploy/finished` gets `halite/node/web1.example/deploy/finished`, and
// one that sends `halite/job/…/ret/…` gets its own copy of that string
// under its own prefix rather than a forgery of the hub's.
func nodeEventTag(nodeID, asked string) (string, error) {
	if asked == "" {
		return "", errors.New("an event needs a tag")
	}
	prefix := "halite/node/" + nodeID + "/"
	tag := prefix + trimTagPrefix(asked)
	if err := eventbus.ValidTag(tag); err != nil {
		return "", err
	}
	return tag, nil
}

// trimTagPrefix removes a leading slash and a `halite/` the sender
// added, so that a tag is not doubled up.
func trimTagPrefix(tag string) string {
	for len(tag) > 0 && tag[0] == '/' {
		tag = tag[1:]
	}
	const root = "halite/"
	if len(tag) > len(root) && tag[:len(root)] == root {
		tag = tag[len(root):]
	}
	return tag
}

// eventStream is GET /v1/events: an operator following the bus.
//
// NDJSON, one object per line, unterminated — the same shape as the
// subscribe stream, so a consumer that can read one can read the other.
func (s *Server) eventStream(w http.ResponseWriter, r *http.Request, principal string) {
	if s.Events == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub keeps no event bus"))
		return
	}
	query := r.URL.Query()
	tags := query["tag"]
	from := query.Get("from")
	if from == "" {
		from = eventbus.Latest
	}
	limit := 200
	if v := query.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	follow := query.Get("follow") == "true"

	if _, _, err := s.Events.Resolve(from); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok && follow {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal,
			errors.New("this connection cannot stream"))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	next := from
	for {
		events, after, err := s.Events.Read(next, tags, limit)
		if err != nil {
			return
		}
		next = after
		for _, e := range events {
			if err := enc.Encode(e); err != nil {
				return
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		if !follow {
			return
		}
		// Woken by an append rather than polling a file that is not
		// changing. The registration happens before the read above on
		// the next turn, so a record written in between is not missed.
		wait := s.Events.Wait()
		select {
		case <-r.Context().Done():
			return
		case <-wait:
		case <-time.After(25 * time.Second):
			// A comment line keeps a proxy from closing an idle
			// stream, and tells a reader the hub is still there.
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}
