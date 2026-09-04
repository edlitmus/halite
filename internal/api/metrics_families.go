package api

import (
	"strconv"
	"time"

	"github.com/edlitmus/halite/internal/metrics"
)

// apiMetrics is what this service knows that the hub does not: who tried
// to authenticate, and whether the hub answered.
type apiMetrics struct {
	authAttempts      *metrics.Counter
	requests          *metrics.Counter
	requestDuration   *metrics.Histogram
	requestsInFlight  *metrics.Gauge
	responseBytes     *metrics.Counter
	hookDeliveries    *metrics.Counter
	hubScrapeFailures *metrics.Counter
	streams           *metrics.Gauge
	streamEvents      *metrics.Counter

	hubRequests *metrics.Counter
	hubDuration *metrics.Histogram

	tokensIssued  *metrics.Counter
	tokensRevoked *metrics.Counter
}

// m answers with the families, declaring them on first use.
//
// On first use rather than at construction for the reason the hub has
// the same arrangement: a Server is filled in field by field, and tying
// the declaration to the router would mean a registry set afterwards
// was ignored for the life of the process.
func (s *Server) m() *apiMetrics {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if s.metrics == nil {
		s.setupMetrics()
	}
	return s.metrics
}

// setupMetrics declares the families. The caller holds metricsMu.
func (s *Server) setupMetrics() {
	r := s.Metrics
	if r == nil {
		s.metrics = &apiMetrics{}
		return
	}
	r.BuildInfo("api")
	s.metrics = &apiMetrics{
		// SPEC 26.2 names `method` and `result`. The method is how the
		// operator authenticated, so an estate moving off local
		// accounts can see whether anything still uses them.
		authAttempts: r.Counter("halite_auth_attempts_total",
			"Authentication attempts, by method and outcome.", "method", "result"),
		requests: r.Counter("halite_api_requests_total",
			"API requests, by route and status code.", "route", "code"),
		requestDuration: r.Histogram("halite_api_request_duration_seconds",
			"Time to answer one API request, by route.", nil, "route"),
		hookDeliveries: r.Counter("halite_api_hook_deliveries_total",
			"Webhook deliveries, by path and outcome.", "path", "result"),
		hubScrapeFailures: r.Counter("halite_api_hub_scrape_failures_total",
			"Times the hub's exposition could not be read for a scrape."),
		streams: r.Gauge("halite_api_event_streams",
			"Event streams open, by transport.", "transport"),
		streamEvents: r.Counter("halite_api_stream_events_total",
			"Events delivered to a subscriber, by transport.", "transport"),

		// In flight rather than only completed, because the shape a
		// stuck service makes is requests arriving and none finishing:
		// the completed counter goes flat and says nothing about
		// whether that is quiet or wedged.
		requestsInFlight: r.Gauge("halite_api_requests_in_flight",
			"Requests being answered right now, by route.", "route"),
		responseBytes: r.Counter("halite_api_response_bytes_total",
			"Bytes written to clients, by route.", "route"),

		// The API is a client of the hub, so half its latency is not
		// its own. Without these the only question the exposition
		// could answer about a slow API was that it was slow.
		hubRequests: r.Counter("halite_api_hub_requests_total",
			"Requests to the hub, by route and status. A zero status is a request that got none.",
			"route", "code"),
		hubDuration: r.Histogram("halite_api_hub_request_duration_seconds",
			"Time one request to the hub took, by route.", nil, "route"),

		tokensIssued: r.Counter("halite_api_tokens_issued_total",
			"Tokens minted, by how the operator authenticated.", "method"),
		tokensRevoked: r.Counter("halite_api_tokens_revoked_total",
			"Tokens revoked by a logout."),
	}

	// The hub client is timed from here rather than from wherever the
	// service was assembled, so that no assembly can leave it out.
	// It was set in the command, and the tests -- which build their own
	// client against a stub hub -- got an exposition with the families
	// declared and no series in them: the shape of an estate that never
	// talks to its hub.
	if s.Hub != nil {
		s.Hub.Observe = s.ObserveHubRequest
	}

	// Read at scrape time rather than kept in step: the store is on
	// disk and is written by `halite-api token` as well as by this
	// service, so a counter held here would drift from it the first
	// time an operator revoked one by hand.
	r.GaugeFunc("halite_api_tokens_live",
		"Tokens that exist and have not expired or been revoked.",
		func() float64 { return float64(s.countLiveTokens()) })
}

// countLiveTokens counts the tokens a login could still be using.
//
// A store that cannot be read reports zero rather than failing the
// scrape: an exposition that errors takes every other metric with it.
func (s *Server) countLiveTokens() int {
	if s.Tokens == nil {
		return 0
	}
	tokens, err := s.Tokens.List()
	if err != nil {
		return 0
	}
	now := s.now()
	n := 0
	for _, t := range tokens {
		if t.Live(now) {
			n++
		}
	}
	return n
}

// ObserveHubRequest is the hook transport.Client calls after each
// request this service makes to the hub.
//
// Exported because the client is assembled by the command and the
// registry belongs to the server; this is the seam between them.
func (s *Server) ObserveHubRequest(route string, status int, took time.Duration) {
	m := s.m()
	m.hubRequests.With(route, strconv.Itoa(status)).Inc()
	if took >= 0 {
		m.hubDuration.With(route).Observe(took.Seconds())
	}
}
