package api

import "github.com/edlitmus/halite/internal/metrics"

// apiMetrics is what this service knows that the hub does not: who tried
// to authenticate, and whether the hub answered.
type apiMetrics struct {
	authAttempts      *metrics.Counter
	requests          *metrics.Counter
	requestDuration   *metrics.Histogram
	hookDeliveries    *metrics.Counter
	hubScrapeFailures *metrics.Counter
	streams           *metrics.Gauge
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
	}
}
