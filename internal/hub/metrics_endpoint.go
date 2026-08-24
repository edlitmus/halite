package hub

import (
	"errors"
	"net/http"

	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// metricsExposition is GET /v1/metrics on the hub.
//
// It is behind the same operator certificate and the same policy as
// every other operator endpoint. SPEC 22.1 says the API's metrics are
// authenticated by default, and the hub's carry more: an unauthenticated
// scrape endpoint on a control plane tells anyone who asks how many
// nodes it has, what functions the estate runs, and when a deployment
// went out.
//
// It is granted as a runner, `metrics.show`, because reading a number
// off the hub is asking the hub a question rather than acting on the
// fleet.
func (s *Server) metricsExposition(w http.ResponseWriter, r *http.Request, principal string) {
	decision := s.Policy.Authorize(policy.Request{
		Principal: principal,
		Fun:       "metrics.show",
		Runner:    true,
	})
	s.countDecision(decision)
	if !decision.Allowed {
		s.info("metrics refused", "principal", principal, "reason", decision.Reason)
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			errors.New(decision.Reason))
		return
	}
	if s.Metrics == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub records no metrics"))
		return
	}
	w.Header().Set("Content-Type", metrics.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := s.Metrics.Write(w); err != nil {
		s.warn("the metrics exposition could not be written", "error", err.Error())
	}
}

// countDecision records one authorization outcome.
//
// Refused and allowed are counted separately because the useful alert is
// a rate of refusals, not a total of decisions.
func (s *Server) countDecision(d policy.Decision) {
	result := "denied"
	if d.Allowed {
		result = "allowed"
	}
	s.m().authzDecisions.With(result).Inc()
}
