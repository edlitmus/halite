package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/websocket"
)

// wsEventStream is `GET /v1/ws/events`: the same stream as `/v1/events`
// over a WebSocket, for a client that would rather have one.
//
// The same filter, deliberately shared: two streams of the same events
// with two authorization paths would be two chances to get it wrong,
// and the one that leaks is the one nobody tested.
func (s *Server) wsEventStream(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	if !s.authorize(w, token, requestFor("event.listen", "", nil, nil, true)) {
		return
	}

	conn, err := websocket.Accept(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, tokenEnded, stopWatching := s.whileTokenHolds(r.Context(), token)
	defer stopWatching()
	defer func() {
		// One close frame, saying why when the token is the reason: a
		// client that reconnects is told again, as a 401.
		code, reason := websocket.CloseGoingAway, ""
		if why := tokenEnded(); why != "" {
			code, reason = websocket.ClosePolicy, closeReason(why)
			s.info("an event stream was ended: its token can no longer be used",
				"principal", token.Principal, "token_id", token.ID, "reason", why)
		}
		_ = conn.Close(code, reason)
	}()

	s.m().streams.With("websocket").Add(1)
	defer s.m().streams.With("websocket").Add(-1)

	tags := tagFilter(r)
	from := r.URL.Query().Get("from")
	if from == "" {
		from = "latest"
	}

	// A reader, so that a ping is answered and a close is noticed. The
	// stream is one-way in practice, and a peer that goes away without
	// this would be discovered only by a write failing much later.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			if _, err := conn.Read(); err != nil {
				cancel()
				return
			}
		}
	}()

	// An idle stream is pinged, so an intermediary that closes quiet
	// connections does not close this one.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.Ping(); err != nil {
					cancel()
					return
				}
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	err = s.Hub.FollowEvents(ctx, tags, from, true, 0, func(raw json.RawMessage) error {
		var e eventbus.Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil
		}
		if !s.visible(token, &e) {
			return nil
		}
		if err := conn.WriteText(raw); err != nil {
			return err
		}
		s.m().streamEvents.With("websocket").Inc()
		return nil
	})
	if err != nil && ctx.Err() == nil {
		s.warn("the websocket event stream ended",
			"principal", token.Principal, "error", err.Error())
	}
}

// closeReason fits a reason into a close frame, whose payload RFC 6455
// section 5.5 bounds at 125 bytes, two of them the code. Cut at a rune.
func closeReason(why string) string {
	const room = 123
	if len(why) <= room {
		return why
	}
	cut := why[:room]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
