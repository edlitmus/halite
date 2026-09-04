package api

import (
	"encoding/json"
	"net/http"
	"time"

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
	defer conn.Close(websocket.CloseGoingAway, "")

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
	ctx, cancel := contextFrom(r)
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
