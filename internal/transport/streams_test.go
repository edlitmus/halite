package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// holdingHub answers health checks only when told to, and counts how
// many it is holding, so a test can see what reached it rather than
// what the client meant to send.
type holdingHub struct {
	inFlight, peak atomic.Int64
	// streams counts long-lived requests the hub is holding open.
	streams  atomic.Int64
	release  chan struct{}
	released sync.Once
}

func newHoldingHub(t *testing.T) (*holdingHub, *Client) {
	t.Helper()
	h := &holdingHub{release: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc(PathHealth, func(w http.ResponseWriter, r *http.Request) {
		now := h.inFlight.Add(1)
		defer h.inFlight.Add(-1)
		for {
			old := h.peak.Load()
			if now <= old || h.peak.CompareAndSwap(old, now) {
				break
			}
		}
		select {
		case <-h.release:
		case <-r.Context().Done():
		}
		w.Write([]byte("ok"))
	})
	// Both long-lived endpoints answer at once and then hold the stream
	// open, which is all a subscribe or a follow is to the transport.
	stream := func(w http.ResponseWriter, r *http.Request) {
		h.streams.Add(1)
		defer h.streams.Add(-1)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
	mux.HandleFunc(PathSubscribe, stream)
	mux.HandleFunc(PathEvents, stream)
	f := newFixture(t, mux)
	pair, _ := f.enrolledPair(t, "web1.example")
	client := &Client{HubURL: f.url, CA: f.ca.Cert, Cert: &pair, Timeout: 30 * time.Second}
	t.Cleanup(func() {
		h.unblock()
		client.CloseIdleConnections()
	})
	return h, client
}

func (h *holdingHub) unblock() { h.released.Do(func() { close(h.release) }) }

func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fill puts n health checks in flight and returns their results once
// they finish.
func fill(client *Client, n int) <-chan error {
	results := make(chan error, n)
	for range n {
		go func() {
			_, err := client.Health(context.Background())
			results <- err
		}()
	}
	return results
}

// A burst bigger than SPEC 6.5's 64 never reaches the hub as more than
// ShortRequestLimit at once, cold connection or not: the request that
// would have been reset is not issued until a slot frees (DIVERGENCE
// 5.665.3). Exactly the limit, too -- a bound that held at 10 would pass
// "at most" and be a slower client for no reason.
func TestABurstReachesTheHubNoMoreThanTheLimitAtOnce(t *testing.T) {
	hub, client := newHoldingHub(t)
	const burst = 100
	results := fill(client, burst)

	until(t, "the hub to hold the first requests", func() bool { return hub.inFlight.Load() >= ShortRequestLimit })
	// Room for any request past the limit to arrive, which without the
	// bound all 100 do at once.
	time.Sleep(200 * time.Millisecond)
	if got := hub.peak.Load(); got != ShortRequestLimit {
		t.Fatalf("the hub held %d requests at once; the client's limit is %d", got, ShortRequestLimit)
	}

	hub.unblock()
	for range burst {
		if err := <-results; err != nil {
			t.Errorf("a request waiting for a slot failed instead of going out: %v", err)
		}
	}
}

// The hazard 748 named before any of this was built: a node whose short
// requests fill every slot must still be able to open its subscribe
// stream, which is the one it cannot operate without.
func TestTheSubscribeStreamOpensWhileEveryShortSlotIsTaken(t *testing.T) {
	hub, client := newHoldingHub(t)
	fill(client, ShortRequestLimit)
	until(t, "every slot to be taken", func() bool { return hub.inFlight.Load() == ShortRequestLimit })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan struct{})
	go client.SubscribeOpened(ctx, SubscribeRequest{NodeID: "web1.example", Grains: json.RawMessage(`{}`)},
		func() { close(opened) }, func(Message) error { return nil })
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the subscribe stream waited behind the short requests")
	}
}

// An event stream that follows the bus holds its stream for as long as
// somebody watches, and the API opens one per watcher through a single
// Client. Counted against the slots, enough watchers would stop every
// other request the API makes; they are not counted.
func TestFollowingEventStreamsTakeNoSlot(t *testing.T) {
	hub, client := newHoldingHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	var watching sync.WaitGroup
	defer watching.Wait()
	defer cancel()
	const watchers = ShortRequestLimit + 1
	for range watchers {
		watching.Add(1)
		go func() {
			defer watching.Done()
			client.FollowEvents(ctx, nil, "", true, 0, func(json.RawMessage) error { return nil })
		}()
	}
	until(t, "every watcher's stream to be open", func() bool { return hub.streams.Load() == watchers })

	fill(client, 1)
	until(t, "a health check to reach the hub past the watchers", func() bool { return hub.inFlight.Load() == 1 })
}

// A request that cannot get a slot gives up when its context does, and
// says the wait was here: it never reached the hub, and a timeout
// reported as the hub's sends somebody to the wrong machine.
func TestARequestWaitingForASlotGivesUpWithItsContextAndSaysSo(t *testing.T) {
	hub, client := newHoldingHub(t)
	fill(client, ShortRequestLimit)
	until(t, "every slot to be taken", func() bool { return hub.inFlight.Load() == ShortRequestLimit })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := client.Health(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the context's deadline", err)
	}
	if !strings.Contains(err.Error(), "request slots") {
		t.Errorf("the error does not say the request waited in this client: %v", err)
	}
	if got := hub.peak.Load(); got != ShortRequestLimit {
		t.Errorf("the request that gave up reached the hub anyway: peak %d", got)
	}
}

// Every way a request can end gives its slot back. A slot kept by a
// failure is a client that stops after its 63rd, so each path is run
// more times than there are slots.
func TestEveryWayARequestEndsGivesItsSlotBack(t *testing.T) {
	t.Run("the hub refuses it", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc(PathJobs, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"no"}`, http.StatusInternalServerError)
		})
		f := newFixture(t, mux)
		pair, _ := f.enrolledPair(t, "web1.example")
		client := &Client{HubURL: f.url, CA: f.ca.Cert, Cert: &pair, Timeout: 5 * time.Second}
		defer client.CloseIdleConnections()
		for i := range 2 * ShortRequestLimit {
			if _, err := client.Submit(context.Background(), SubmitRequest{Target: "*", Fun: "test.ping"}); err == nil {
				t.Fatalf("request %d: the hub refused it and the client reported success", i)
			}
		}
	})
	t.Run("it never reaches a hub", func(t *testing.T) {
		f := newFixture(t, http.NotFoundHandler())
		pair, _ := f.enrolledPair(t, "web1.example")
		// A port nothing listens on: the dial fails, so RoundTrip
		// returns an error and no body.
		client := &Client{HubURL: "https://127.0.0.1:1", CA: f.ca.Cert, Cert: &pair, Timeout: 5 * time.Second}
		for i := range 2 * ShortRequestLimit {
			if _, err := client.Health(context.Background()); err == nil {
				t.Fatalf("request %d reached a port nothing listens on", i)
			} else if strings.Contains(err.Error(), "request slots") {
				t.Fatalf("request %d waited for a slot a failed dial kept: %v", i, err)
			}
		}
	})
}
