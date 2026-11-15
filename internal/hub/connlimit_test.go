package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// subscribed opens a stream for client and waits until the hub has
// attached it, answering with a cancel that ends it and a channel that
// carries whatever error the stream ended with.
func subscribed(t *testing.T, l *lab, client *transport.Client, nodeID string) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opened := make(chan struct{})
	ended := make(chan error, 1)
	go func() {
		ended <- client.SubscribeOpened(ctx, transport.SubscribeRequest{NodeID: nodeID, Version: "test"},
			func() { close(opened) }, func(transport.Message) error { return nil })
	}()
	select {
	case <-opened:
	case err := <-ended:
		t.Fatalf("%s could not subscribe: %v", nodeID, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("%s's stream never opened", nodeID)
	}
	return cancel, ended
}

// subscribeOnce makes one subscribe and answers with how it ended, or
// nil if the stream opened -- in which case it is ended at once.
func subscribeOnce(client *transport.Client, nodeID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opened := false
	err := client.SubscribeOpened(ctx, transport.SubscribeRequest{NodeID: nodeID, Version: "test"},
		func() { opened = true; cancel() }, func(transport.Message) error { return nil })
	if opened {
		return nil
	}
	return err
}

// SPEC 6.5: "Maximum node connections per hub | 25,000, configured".
// Nothing enforced it; the handbook said "default max 25,000 per hub".
func TestTheHubRefusesANodeOverItsConnectionCap(t *testing.T) {
	reg := metrics.NewRegistry()
	l := newLabWith(t, func(s *Server) {
		s.MaxNodeConnections = 2
		s.Metrics = reg
	})
	a := l.enrolled(t, "a.example")
	b := l.enrolled(t, "b.example")
	c := l.enrolled(t, "c.example")

	_, _ = subscribed(t, l, a, "a.example")
	endB, endedB := subscribed(t, l, b, "b.example")

	err := subscribeOnce(c, "c.example")
	if err == nil {
		t.Fatal("a third node subscribed to a hub capped at two")
	}
	var status *transport.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusServiceUnavailable {
		t.Fatalf("the refusal was %v (%T), want a 503", err, err)
	}
	if code := transport.CodeOf(err); code != transport.CodeHubFull {
		t.Errorf("the refusal's code is %q, want %q", code, transport.CodeHubFull)
	}
	if !strings.Contains(err.Error(), "max_node_connections") {
		t.Errorf("the refusal does not name the setting an operator would change: %v", err)
	}
	// What the node does with it. An outage: run on from the cache and
	// reconnect. Not a refusal of this node, and above all not a
	// rejected certificate, which makes a node forget its pillar.
	if !transport.Unreachable(err) {
		t.Errorf("a node would not treat a full hub as an outage: %v", err)
	}
	var refused *transport.RefusedError
	if errors.As(err, &refused) || transport.Rejected(err) {
		t.Errorf("a full hub reads to the node as a refusal of the node: %v", err)
	}
	if _, ok := l.server.fleet().Connected()["c.example"]; ok {
		t.Error("the refused node is in the fleet")
	}
	if n := len(l.server.fleet().Connected()); n != 2 {
		t.Errorf("the fleet holds %d streams, want 2", n)
	}

	// A node already holding a stream reconnects at the cap: its new
	// stream replaces the old one and the count does not move.
	if err := subscribeOnce(a, "a.example"); err != nil {
		t.Errorf("a connected node could not reconnect to a full hub: %v", err)
	}

	// A node leaving makes room.
	endB()
	select {
	case <-endedB:
	case <-time.After(5 * time.Second):
		t.Fatal("b's stream did not end")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := l.server.fleet().Connected()["b.example"]; !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the hub still holds b's stream")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := subscribeOnce(c, "c.example"); err != nil {
		t.Errorf("c was refused after b left: %v", err)
	}

	var out bytes.Buffer
	if err := reg.Write(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "halite_hub_node_connect_refused_total 1\n") {
		t.Errorf("the refusal was not counted once:\n%s", grepLines(out.String(), "refused"))
	}
}

// The pre-check in subscribe is advisory; the attach decides. Fifty
// nodes arriving at once for ten slots get exactly ten.
func TestTheCapHoldsWhenNodesArriveTogether(t *testing.T) {
	f := newFleet()
	const max, nodes = 10, 50
	var won atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, ok := f.attachLimited(fmt.Sprintf("n%d.example", i), nowForTest(), max); ok {
				won.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := won.Load(); got != max {
		t.Errorf("%d of %d nodes attached to a fleet capped at %d", got, nodes, max)
	}
	if got := len(f.Connected()); got != max {
		t.Errorf("the fleet holds %d streams, want %d", got, max)
	}
}

// A hub configured with nothing uses SPEC's number, not "no limit".
func TestTheConnectionCapDefaultsToSPECs(t *testing.T) {
	for _, configured := range []int{0, -1} {
		s := &Server{MaxNodeConnections: configured}
		if got := s.maxNodeConnections(); got != 25000 {
			t.Errorf("max_node_connections %d gives %d, want SPEC 6.5's 25,000", configured, got)
		}
	}
	if got := (&Server{MaxNodeConnections: 7}).maxNodeConnections(); got != 7 {
		t.Errorf("max_node_connections 7 gives %d", got)
	}
}

// SPEC 6.5: "Maximum concurrent HTTP/2 streams per node connection |
// 64". net/http advertises 250 unless told otherwise, and nothing told
// it.
//
// Counted per connection, on the hub's own server settings with a
// handler that holds every request open. The client is not held to one
// connection, because it cannot be: a Go client at a server's stream
// limit dials another, and MaxConnsPerHost does not stop it once the
// first has become HTTP/2 -- measured here, it opened a second
// connection with MaxConnsPerHost set to 1. So the requests over the
// limit land on a second connection, and what is checked is that no
// connection carried more than 64 at once.
// streamResetAttempts is how many times a request is sent before the
// test gives up on it. Three, because the thing being absorbed costs one
// retry: a reset stream is retried onto a connection that has room, and
// needing a third attempt means something other than the stream limit is
// wrong.
//
// It bounds attempts rather than time on purpose. A duration here would
// be a guess about a machine's speed; a count is a statement about the
// mechanism.
const streamResetAttempts = 3

// getRetryingAResetStream sends a request again when the server reset the
// stream, which is what a client that wanted an answer would do, and
// counts how often it had to.
//
// # Why this is needed, measured rather than assumed
//
// **Go's HTTP/2 client over-commits streams past a limit it has already
// acknowledged, and Go's server then kills one of them outright.** The
// failure was `only 79 of 80 requests reached the hub`, about 5 times in
// 1200 runs of this test, always preceded by a stream error on a stream
// past the 64th. It is nothing to do with this project: a ~100-line
// program using only `net/http`, with `HTTP2Config{MaxConcurrentStreams:
// 64}` and 80 requests held open, reproduces it at the same rate.
//
// The frame log says exactly what happens. The server sends
// `MAX_CONCURRENT_STREAMS=64`; the client reads it and acknowledges it;
// the client then opens more than 64 anyway. The ones arriving before the
// server reads that acknowledgement come back `REFUSED_STREAM`, which the
// client retries by itself, invisibly and harmlessly. The first one
// arriving *after* it comes back `PROTOCOL_ERROR` — the branch Go's own
// server comments "They should know better" — and `PROTOCOL_ERROR` is not
// retried, so that request simply dies. Known upstream as golang/go#13774
// and #16582, with #39389 and #47635 adjacent.
//
// # Why retrying, rather than asking for less
//
// The claim this test exists for is the server's: no connection carries
// more than SPEC 6.5's 64 streams at once, and the assertion on the peak
// is untouched by this. The arrival count is a separate guard — it is what
// would catch a hub that refused the second connection, where the peak
// would still be a blameless 64 — so weakening it to "79 will do" would
// have cost a real check to avoid a borrowed defect.
//
// Retrying on **any** error, rather than on the two codes above, is
// deliberate. Matching the error's text would make the retry stop
// silently if Go reworded it, and the flake would come back at 0.4% with
// nothing to show why. Anything that is actually broken — a refused
// connection, a bad certificate, a GOAWAY — fails every attempt and still
// fails the test, while the retry count is logged so that a reader is
// never told there was no trouble. DIVERGENCE 5.665.
func getRetryingAResetStream(client *http.Client, url string, retried *atomic.Int64) (*http.Response, error) {
	var err error
	for attempt := 1; attempt <= streamResetAttempts; attempt++ {
		var res *http.Response
		res, err = client.Get(url)
		if err == nil {
			return res, nil
		}
		retried.Add(1)
	}
	return nil, fmt.Errorf("after %d attempts: %w", streamResetAttempts, err)
}

func TestTheHubAllowsSPECsConcurrentStreamsPerConnection(t *testing.T) {
	l := newLab(t)

	type connKey struct{}
	type conn struct{ inside, peak atomic.Int64 }
	var mu sync.Mutex
	var all []*conn
	release := make(chan struct{})
	var arrived atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := r.Context().Value(connKey{}).(*conn)
		n := c.inside.Add(1)
		for {
			p := c.peak.Load()
			if n <= p || c.peak.CompareAndSwap(p, n) {
				break
			}
		}
		arrived.Add(1)
		<-release
		c.inside.Add(-1)
	})

	pair, err := l.files.KeyPair(pki.HubCertFile, pki.HubKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen("127.0.0.1:0", NewServingCert(pair), l.ca.Cert, l.denied)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := newHTTPServer(ctx, handler)
	srv.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		c := &conn{}
		mu.Lock()
		all = append(all, c)
		mu.Unlock()
		return context.WithValue(ctx, connKey{}, c)
	}
	go srv.Serve(ln)
	defer srv.Close()

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   transport.EnrollConfig(l.ca.Cert, "127.0.0.1"),
		ForceAttemptHTTP2: true,
	}}
	url := "https://" + ln.Addr().String() + "/"
	const tries = transport.MaxConcurrentStreams + 16
	var retried atomic.Int64
	var wg sync.WaitGroup
	for range tries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := getRetryingAResetStream(client, url, &retried)
			if err != nil {
				t.Error(err)
				return
			}
			res.Body.Close()
			if res.ProtoMajor != 2 {
				t.Errorf("the request went over HTTP/%d", res.ProtoMajor)
			}
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for arrived.Load() < tries && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := arrived.Load()
	close(release)
	wg.Wait()
	if n := retried.Load(); n > 0 {
		t.Logf("%d of %d requests were sent again after the server reset the stream", n, tries)
	}
	if got != tries {
		t.Fatalf("only %d of %d requests reached the hub", got, tries)
	}

	mu.Lock()
	defer mu.Unlock()
	var most int64
	for _, c := range all {
		most = max(most, c.peak.Load())
	}
	if most != transport.MaxConcurrentStreams {
		t.Errorf("one connection carried %d streams at once, want SPEC 6.5's %d (connections: %d)",
			most, transport.MaxConcurrentStreams, len(all))
	}
}
