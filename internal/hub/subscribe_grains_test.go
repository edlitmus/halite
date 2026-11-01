package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// A subscribe that reports a version and no grains leaves the grains the hub
// holds, with the age they had.
//
// The node sends no grains when it cannot encode them (`grainsJSON` logs and
// returns nothing), and the hub wrote that over its cache, so every target on
// a grain matched nothing for that node until its next refresh. DIVERGENCE
// 5.523.
func TestAReconnectWithoutGrainsDoesNotEraseTheCachedOnes(t *testing.T) {
	nodes, err := OpenNodeCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	s := &Server{Nodes: nodes, Now: func() time.Time { return clock }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.subscribe(w, r, r.Header.Get("X-Test-Node"))
	}))
	t.Cleanup(srv.Close)

	stop, ended := openSubscribe(t, srv.URL, "web1.example", transport.SubscribeRequest{
		Grains: json.RawMessage(`{"os":"FreeBSD"}`), Version: "0.1.0",
	})
	reported := clock
	stop()
	<-ended

	clock = clock.Add(2 * time.Hour)
	openSubscribe(t, srv.URL, "web1.example", transport.SubscribeRequest{Version: "0.1.1"})
	eventually(t, "the second report to be recorded", func() bool {
		d, err := nodes.Get("web1.example")
		return err == nil && d.Version == "0.1.1"
	})

	d, err := nodes.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(d.Grains, &got); err != nil || got["os"] != "FreeBSD" {
		t.Errorf("the cached grains are %q after a report with none", d.Grains)
	}
	if !d.LastSeen.Equal(reported) {
		t.Errorf("the grains' age was reset to %v; they were reported at %v", d.LastSeen, reported)
	}
}
