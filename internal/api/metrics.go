package api

import (
	"net/http"
	"strings"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/policy"
)

// metricsExposition is `GET /v1/metrics`, SPEC 22.1's Prometheus
// endpoint, authenticated by default.
//
// This is the estate's scrape target, because it is the only part of
// the control plane a scraper can reach: the hub speaks its own ALPN
// protocol over mutual TLS, which no scraper does. So the answer is
// both expositions — the service's own, and the hub's fetched under the
// service's certificate.
//
// Authorized twice, like every other request here: `metrics.show` for
// the operator behind the token at this end, and the service's own
// certificate at the hub. An estate that grants the service no metrics
// grant gets its API's own numbers and a comment saying why the rest
// are missing, rather than a body that looks like a healthy hub with
// nothing happening.
func (s *Server) metricsExposition(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	if !s.authorize(w, token, policy.Request{Fun: "metrics.show", Runner: true}) {
		return
	}

	// The hub is asked first, so that a scrape which could not reach it
	// reports that in its own body: counting the failure after this
	// service's numbers were rendered would put it one scrape late,
	// which is one scrape too late for the alert it exists to raise.
	fromHub := s.hubExposition(r)

	var own strings.Builder
	if s.Metrics != nil {
		if err := s.Metrics.Write(&own); err != nil {
			writeError(w, http.StatusInternalServerError, "the exposition could not be written")
			return
		}
	}
	// Merged rather than concatenated. Both components expose
	// `halite_build_info` — which is what its `component` label is for
	// — and two `# HELP` lines for one metric name make the whole
	// document invalid, so a scraper answers "no metrics at all"
	// rather than "one duplicated family".
	body := metrics.Merge(own.String(), fromHub)

	w.Header().Set("Content-Type", metrics.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(body)); err != nil {
		s.warn("the exposition could not be sent", "error", err.Error())
	}
}

// hubExposition fetches the hub's metrics, or explains in a comment why
// it could not.
//
// A comment rather than an error: a scrape that fails entirely because
// the hub is unreachable loses the API's own metrics too, and one of
// those is how often the hub is unreachable. `#` is a comment in the
// exposition format, so a scraper ignores it and a person reading the
// body by hand sees the reason.
func (s *Server) hubExposition(r *http.Request) string {
	if s.Hub == nil {
		return "# the hub's metrics are absent: this service is not configured with a hub\n"
	}
	out, err := s.Hub.Metrics(r.Context())
	if err != nil {
		s.warn("the hub's metrics could not be read", "error", err.Error())
		s.m().hubScrapeFailures.Inc()
		return "# the hub's metrics are absent: " + comment(err.Error()) + "\n"
	}
	return out
}

// comment flattens a message onto one line, so an error containing a
// newline cannot produce a second line that is not a comment.
func comment(msg string) string {
	return strings.ReplaceAll(strings.ReplaceAll(msg, "\r", " "), "\n", " ")
}
