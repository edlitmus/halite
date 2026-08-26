package relay

import "github.com/edlitmus/halite/internal/metrics"

// Register declares this relay's families on a registry.
//
// SPEC 26.2 asks for a counter on every bounded queue and every drop
// path. The spool is both: it is capped, and a return it cannot hold is
// refused. A relay whose spool is quietly filling is the failure an
// operator most needs to see before it matters, because the symptom
// upstream is only that some returns are late.
func (r *Relay) Register(reg *metrics.Registry) {
	if reg == nil {
		return
	}
	r.returnsUp = reg.Counter("halite_relay_returns_forwarded_total",
		"Subordinate returns handed to the upstream, by outcome.", "result")
	r.eventsUp = reg.Counter("halite_relay_events_forwarded_total",
		"Events forwarded upstream because their tag matched relay_event_tags.")

	reg.GaugeFunc("halite_relay_spool_entries",
		"Returns waiting for an upstream that could not take them.", func() float64 {
			return float64(r.spool.Count())
		})
	reg.GaugeFunc("halite_relay_spool_dropped_total",
		"Returns the spool refused because it was full.", func() float64 {
			return float64(r.spool.Dropped())
		})
	reg.GaugeFunc("halite_relay_upstream_connected",
		"1 when the upstream stream is open.", func() float64 {
			if r.isConnected() {
				return 1
			}
			return 0
		})
	reg.GaugeFunc("halite_relay_subordinates",
		"Nodes this relay proxies for.", func() float64 {
			return float64(len(r.fleet().Connected()))
		})
}
