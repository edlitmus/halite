package hub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/metrics"
)

// A path the hub does not serve is not a route. `hubRoute` labelled a
// request with its path, normalising only the jobs and files prefixes, and
// the catch-all that answers an unknown path is reachable with no client
// certificate: a scanner asking for a few hundred different paths used up
// the family's whole series bound, after which every route first seen
// later was counted under `__overflow__`, and the per-route request and
// latency series an operator reads could no longer be told apart. The paths
// are also whatever the caller typed, in a label on an exposition.
func TestUnknownPathsShareOneRouteLabel(t *testing.T) {
	reg := metrics.NewRegistry()
	s := &Server{Metrics: reg}
	h := s.Handler()

	for i := 0; i < metrics.MaxSeries+100; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/scan/%d", i), nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("/scan/%d answered %d", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/health", nil))

	var out strings.Builder
	reg.Write(&out)
	text := out.String()
	if strings.Contains(text, "/scan/") {
		t.Error("a path nobody serves became a route label")
	}
	if strings.Contains(text, metrics.OverflowLabel) {
		t.Errorf("unknown paths filled the route family and spilled into %s", metrics.OverflowLabel)
	}
	if !strings.Contains(text, `halite_hub_requests_total{route="/v1/health",code="200"} 1`) {
		t.Errorf("a real route is not counted under its own label:\n%s", section(text, "halite_hub_requests_total"))
	}
}
