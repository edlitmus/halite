package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/eventbus"
)

// visible decides whether a token's holder may see one event.
//
// SPEC 17.4 filters the stream by the caller's policy so that a caller
// cannot subscribe to events about nodes it may not see. An event that
// names a node is filtered by target coverage; one that does not — a
// job's, a key's, the reactor's own — is not about a node, so it goes
// to anyone the policy grants anything.
func (s *Server) visible(token *apitoken.Token, e *eventbus.Event) bool {
	node := eventbus.NodeOf(e)
	if node == "" {
		return s.Policy.HasAnyRule(token.Roles)
	}
	return s.Policy.VisibleNode(token.Roles, node)
}

// eventStream is `GET /v1/events`: Server-Sent Events.
//
// SSE rather than a bespoke framing because it is what a browser and
// every HTTP client already speak, and because a stream that reconnects
// carries its own resumption in `Last-Event-ID` — which maps exactly
// onto the bus offsets of SPEC 17.2, so a reconnecting consumer loses
// nothing.
func (s *Server) eventStream(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	if !s.authorize(w, token, requestFor("event.listen", "", nil, nil, true)) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "this connection cannot stream")
		return
	}

	s.m().streams.With("sse").Add(1)
	defer s.m().streams.With("sse").Add(-1)

	tags := tagFilter(r)
	from := r.URL.Query().Get("from")
	// A reconnecting client sends the offset it last saw, which is
	// what makes the stream lossless across a disconnection.
	if resume := r.Header.Get("Last-Event-ID"); resume != "" {
		from = resume
	}
	if from == "" {
		from = "latest"
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	err := s.Hub.FollowEvents(r.Context(), tags, from, true, 0, func(raw json.RawMessage) error {
		var e eventbus.Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil
		}
		if !s.visible(token, &e) {
			return nil
		}
		// The offset is the SSE id, so `Last-Event-ID` on a
		// reconnection resumes exactly where this left off.
		if e.Offset != "" {
			fmt.Fprintf(w, "id: %s\n", e.Offset)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Tag, raw)
		flusher.Flush()
		s.m().streamEvents.With("sse").Inc()
		return nil
	})
	if err != nil && r.Context().Err() == nil {
		s.warn("the event stream ended", "principal", token.Principal, "error", err.Error())
	}
}

// tagFilter reads the tag globs a caller asked for.
func tagFilter(r *http.Request) []string {
	var out []string
	for _, raw := range r.URL.Query()["tag"] {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// heartbeat is how long an idle SSE stream waits before a comment is
// sent, so that an intermediary does not close a connection it thinks
// is dead.
const heartbeat = 30 * time.Second

// contextFrom derives a cancellable context from a request, so a
// stream can end itself when the peer goes away.
func contextFrom(r *http.Request) (ctx context.Context, cancel context.CancelFunc) {
	return context.WithCancel(r.Context())
}
