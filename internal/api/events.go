package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/eventbus"
)

// visible decides whether a token's holder may see one event.
//
// SPEC 17.4 filters the stream by the caller's policy so that a caller
// cannot subscribe to events about nodes it may not see. An event that
// names a node is filtered by target coverage.
//
// One that does not is not about a node, and used to go to anyone the
// policy grants anything. That was right for a key's or the reactor's own
// and wrong for a job's creation, which carries the function, its
// arguments and the nodes it reached, and for a runner call's, which
// carries its arguments: a caller scoped to web* read what was run on
// db1 (issue 886). Those are filtered by what they are about, by the rule
// the hub's event runners share (eventbus.Subject).
func (s *Server) visible(token *apitoken.Token, e *eventbus.Event) bool {
	node := eventbus.NodeOf(e)
	if node != "" {
		return s.Policy.VisibleNode(token.Roles, node)
	}
	if subject, judged := eventbus.SubjectOf(e); judged {
		return subject.VisibleTo(s.readerFor(token))
	}
	return s.Policy.HasAnyRule(token.Roles)
}

// readerFor is what the event stream knows about a token's holder, for
// judging a job or a run.
//
// A runner call made through the API is recorded with the API as its
// principal and the operator as the one it is for, so the operator is
// the caller of a run when it is named as either. A run the operator made
// directly names it as the principal and no one it is for.
func (s *Server) readerFor(token *apitoken.Token) eventbus.Reader {
	return eventbus.Reader{
		Everything: s.Policy.SeesEveryNode(token.Roles),
		Node: func(node string) bool {
			return s.Policy.VisibleNode(token.Roles, node)
		},
		Made: func(principal, onBehalfOf string) bool {
			if onBehalfOf != "" {
				return onBehalfOf == token.Principal
			}
			return principal == token.Principal
		},
	}
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

	ctx, tokenEnded, stopWatching := s.whileTokenHolds(r.Context(), token)
	defer stopWatching()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	err := s.Hub.FollowEvents(ctx, tags, from, true, 0, func(raw json.RawMessage) error {
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
	if why := tokenEnded(); why != "" {
		// A comment, which every SSE client ignores, so the reason reaches
		// anyone reading the raw stream; a client that reconnects is told
		// again, as a 401, by the token check.
		fmt.Fprintf(w, ": %s; this stream has ended\n\n", why)
		flusher.Flush()
		s.info("an event stream was ended: its token can no longer be used",
			"principal", token.Principal, "token_id", token.ID, "reason", why)
		return
	}
	if err != nil && r.Context().Err() == nil {
		s.warn("the event stream ended", "principal", token.Principal, "error", err.Error())
	}
}

// defaultTokenRecheck is how often an open event stream asks the token
// store whether its token can still be used.
const defaultTokenRecheck = 5 * time.Second

// whileTokenHolds derives a context that ends when the token a stream was
// opened with can no longer be used, and reports why it ended.
//
// A request's token is redeemed once, as it arrives, and an event stream
// is one request that lasts until somebody closes it. So a stream outlived
// a logout, a `halite-api token revoke` and the token's own expiry, and
// kept delivering the fleet's events to whoever held it (GHSA-cg5g-h9vh-8h3x,
// DIVERGENCE 5.685). The store is read rather than told, because a
// revocation is as often made by `halite-api token revoke`, another
// process, as by this one.
//
// The idle limit is not applied while the stream is open: an open stream
// is the token in use. Revocation, the absolute expiry and a record that is
// gone end it, and so does a store that cannot be read, because the
// alternative is a stream nobody can end.
func (s *Server) whileTokenHolds(parent context.Context, token *apitoken.Token) (context.Context, func() string, func()) {
	ctx, cancel := context.WithCancel(parent)
	var mu sync.Mutex
	var why string
	every := s.tokenRecheck
	if every <= 0 {
		every = defaultTokenRecheck
	}
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if reason := s.streamTokenUnusable(token); reason != "" {
					mu.Lock()
					why = reason
					mu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	reason := func() string {
		mu.Lock()
		defer mu.Unlock()
		return why
	}
	return ctx, reason, cancel
}

// streamTokenUnusable says why a stream's token can no longer be used, or
// "" while it can.
func (s *Server) streamTokenUnusable(token *apitoken.Token) string {
	current, err := s.Tokens.Get(token.ID)
	if errors.Is(err, apitoken.ErrNotFound) {
		return "this token no longer exists"
	}
	if err != nil {
		return "this token could not be checked: " + err.Error()
	}
	held := *current
	held.LastUsed = time.Time{} // in use for as long as the stream is open
	return held.Why(s.now())
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
