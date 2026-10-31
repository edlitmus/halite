package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/transport"
)

// openSubscribe opens a subscribe stream as nodeID and returns once the hub
// has answered, with a function that reads until the stream ends.
func openSubscribe(t *testing.T, url, nodeID string, req transport.SubscribeRequest) (cancel func(), ended <-chan struct{}) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	body, _ := json.Marshal(req)
	hreq, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	hreq.Header.Set("X-Test-Node", nodeID)
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	t.Cleanup(stop)
	return stop, done
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A relay that reconnects while the hub still holds its old stream open --
// the usual shape of a network partition, where the hub has not yet noticed
// the peer is gone -- replaces that stream. The old handler then returns and
// ran `dropRelay`, which forgets every subordinate of that relay *by name*,
// including the ones the new connection had just registered. The relay's
// nodes were then unreachable until the relay next sent an update, which it
// does only when its own fleet changes. The same exit also emitted
// `halite/node/<relay>/stop` for a relay that was, at that moment, connected.
func TestAReplacedRelayStreamDoesNotTakeTheNewOnesRoutesWithIt(t *testing.T) {
	bus, err := eventbus.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bus.Close() })
	s := &Server{
		AcceptRelays:      true,
		Policy:            allowEverything(t),
		RelaySubordinates: map[string][]string{"relay1.example": {"web*"}},
		Events:            bus,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.subscribe(w, r, r.Header.Get("X-Test-Node"))
	}))
	t.Cleanup(srv.Close)

	req := transport.SubscribeRequest{
		Relay:        true,
		Subordinates: []transport.Subordinate{{NodeID: "web1.example"}},
	}
	_, firstEnded := openSubscribe(t, srv.URL, "relay1.example", req)
	eventually(t, "the first stream's routes", func() bool {
		_, ok := s.fleet().RelayFor("web1.example")
		return ok
	})

	openSubscribe(t, srv.URL, "relay1.example", req)
	select {
	case <-firstEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced stream never ended")
	}
	// The old handler's deferred work runs after its body closes; give it
	// the moment it needs rather than asserting on the instant of EOF.
	time.Sleep(200 * time.Millisecond)

	if _, ok := s.fleet().RelayFor("web1.example"); !ok {
		t.Error("the relay reconnected and its subordinate is unreachable: the old stream's exit dropped the new one's routes")
	}
	if _, ok := s.fleet().Connected()["web1.example"]; !ok {
		t.Error("a node behind a connected relay does not count as connected")
	}
	events, _, err := bus.Read(eventbus.Earliest, []string{"halite/node/relay1.example/stop"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("a stop event was published for a relay that is connected: %d", len(events))
	}
}

// A node that leaves reports the fleet without it. The count was taken while
// the node was still attached, so the last node of an estate leaving said one
// was connected.
func TestAPresenceChangeForALeavingNodeCountsTheFleetWithoutIt(t *testing.T) {
	bus, err := eventbus.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bus.Close() })
	s := &Server{Events: bus}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.subscribe(w, r, r.Header.Get("X-Test-Node"))
	}))
	t.Cleanup(srv.Close)

	stop, ended := openSubscribe(t, srv.URL, "web1.example", transport.SubscribeRequest{})
	eventually(t, "the node to attach", func() bool { return len(s.fleet().Connected()) == 1 })
	stop()
	<-ended

	var left map[string]any
	eventually(t, "the leaving event", func() bool {
		events, _, err := bus.Read(eventbus.Earliest, []string{"halite/presence/change"}, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Data["left"] == "web1.example" {
				left = e.Data
			}
		}
		return left != nil
	})
	if got := fmt.Sprint(left["connected"]); got != "0" {
		t.Errorf("a node left an estate of one and the event says %v connected", got)
	}
}
