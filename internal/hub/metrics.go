package hub

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/keystore"

	"github.com/edlitmus/halite/internal/metrics"
)

// hubMetrics is every family the hub exposes, declared in one place.
//
// SPEC 26.2 lists them by family, and the rule it states is the reason
// they are gathered here rather than declared where they are used:
// every bounded queue and every drop path has a counter, and a rule
// like that is checkable only if the whole set is visible at once.
type hubMetrics struct {
	nodeConnects    *metrics.Counter
	nodeDisconnects *metrics.Counter

	jobsDispatched  *metrics.Counter
	jobDuration     *metrics.Histogram
	jobReturns      *metrics.Counter
	jobsExpired     *metrics.Counter
	jobsOutstanding *metrics.Gauge

	stateStates  *metrics.Counter
	stateChanges *metrics.Counter

	pillarCompile *metrics.Histogram
	pillarFailure *metrics.Counter

	fileRequests *metrics.Counter
	fileBytes    *metrics.Counter

	eventsPublished *metrics.Counter
	eventsDropped   *metrics.Counter

	reactorDropped  *metrics.Counter
	reactorDuration *metrics.Histogram
	reactorFailures *metrics.Counter

	beaconEvents *metrics.Counter

	authzDecisions *metrics.Counter
}

// setupMetrics declares the families. The caller holds metricsMu.
func (s *Server) setupMetrics() {
	r := s.Metrics
	if r == nil {
		// An empty set rather than nil, so this runs once rather than
		// on every observation a hub without a registry makes.
		s.metrics = &hubMetrics{}
		return
	}
	r.BuildInfo("hub")
	m := &hubMetrics{
		nodeConnects: r.Counter("halite_hub_node_connect_total",
			"Node subscribe streams opened."),
		nodeDisconnects: r.Counter("halite_hub_node_disconnect_total",
			"Node subscribe streams closed, by why they closed.", "reason"),

		jobsDispatched: r.Counter("halite_jobs_dispatched_total",
			"Jobs dispatched, by function.", "fun"),
		jobDuration: r.Histogram("halite_job_duration_seconds",
			"Time from dispatch to a node's return.", nil, "fun"),
		jobReturns: r.Counter("halite_job_returns_total",
			"Returns filed, by whether the node reported success.", "result"),
		jobsExpired: r.Counter("halite_jobs_expired_total",
			"Jobs that reached their time to live before being delivered."),
		// A gauge moved on dispatch and on return, rather than a scan
		// of the job cache at scrape time: a metrics endpoint that
		// reads every job record is a second load on a hub that is
		// already the one under investigation.
		jobsOutstanding: r.Gauge("halite_jobs_missing_returns",
			"Nodes a dispatched job has not yet heard from."),

		stateStates: r.Counter("halite_state_states_total",
			"Individual states reported by a node, by result.", "result"),
		stateChanges: r.Counter("halite_state_changes_total",
			"States that reported a change."),

		pillarCompile: r.Histogram("halite_pillar_compile_duration_seconds",
			"Time to compile one node's pillar on the hub.", nil),
		pillarFailure: r.Counter("halite_pillar_failures_total",
			"Pillar compilations that failed."),

		fileRequests: r.Counter("halite_fileserver_requests_total",
			"File server requests, by backend and status code.", "backend", "code"),
		fileBytes: r.Counter("halite_fileserver_bytes_total",
			"Bytes served from the file server."),

		eventsPublished: r.Counter("halite_events_published_total",
			"Events appended to the bus, by the first two segments of the tag.", "tag_prefix"),
		eventsDropped: r.Counter("halite_events_dropped_total",
			"Events that did not reach the bus.", "reason"),

		reactorDropped: r.Counter("halite_reactor_dropped_total",
			"Events dropped from a reactor worker's queue because it was full."),
		reactorDuration: r.Histogram("halite_reactor_duration_seconds",
			"Time to run one reaction.", nil, "tag_prefix"),
		reactorFailures: r.Counter("halite_reactor_failures_total",
			"Reactions that failed, by why.", "reason"),

		beaconEvents: r.Counter("halite_beacon_events_total",
			"Beacon events received from nodes, by beacon.", "beacon"),

		authzDecisions: r.Counter("halite_authz_decisions_total",
			"Authorization decisions, by outcome.", "result"),
	}
	s.metrics = m

	r.GaugeFunc("halite_hub_nodes_connected",
		"Nodes with a live subscribe stream.", func() float64 {
			return float64(len(s.fleet().Connected()))
		})
	r.GaugeFunc("halite_hub_keys_accepted",
		"Nodes holding an issued certificate.", func() float64 {
			return float64(s.countKeys(keystore.Accepted))
		})
	r.GaugeFunc("halite_hub_keys_pending",
		"Enrollment requests waiting for a decision.", func() float64 {
			return float64(s.countKeys(keystore.Pending))
		})
}

// countKeys counts the key store records in one state.
func (s *Server) countKeys(want keystore.State) int {
	if s.Authority == nil || s.Authority.Store == nil {
		return 0
	}
	records, err := s.Authority.Store.List()
	if err != nil {
		return 0
	}
	n := 0
	for _, rec := range records {
		if rec.State == want {
			n++
		}
	}
	return n
}

// m answers with the metrics, declaring them on first use.
//
// On first use rather than at construction because a Server is a struct
// an operator fills in field by field: tying the declaration to Handler
// or to Serve would mean a registry set afterwards was silently ignored
// for the life of the process, which is the kind of ordering trap that
// is discovered when the graph is empty during an incident.
//
// A hub with no registry gets an empty set, and every method on a nil
// metric does nothing, so no caller needs a check.
func (s *Server) m() *hubMetrics {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if s.metrics == nil {
		s.setupMetrics()
	}
	return s.metrics
}

// tagPrefix is the first two segments of an event tag.
//
// The whole tag would be a series per job, which is exactly the
// unbounded label the registry's own bound exists to survive; the first
// segment alone is always `halite`. Two segments name the kind of thing
// that happened, which is the question a metric answers.
func tagPrefix(tag string) string {
	segments := 0
	for i := 0; i < len(tag); i++ {
		if tag[i] != '/' {
			continue
		}
		segments++
		if segments == 2 {
			return tag[:i]
		}
	}
	return tag
}

// observeSeconds records a duration, guarding against a negative one: a
// clock that moved backwards would otherwise be recorded as a job that
// took less than no time.
func observeSeconds(h *metrics.Histogram, d time.Duration) {
	if d < 0 {
		return
	}
	h.Observe(d.Seconds())
}

// countBeaconEvent records a beacon event by which beacon fired it.
//
// A node's beacon events arrive here as ordinary events, so the hub
// counts them without the node reporting a metric of its own. What it
// cannot see is an event the node's own queue dropped before sending;
// that is counted on the node, which has nowhere to expose it yet.
func (s *Server) countBeaconEvent(tag string) {
	const prefix = "halite/beacon/"
	if !strings.HasPrefix(tag, prefix) {
		return
	}
	// `halite/beacon/<node_id>/<beacon>/...` — the beacon is the
	// segment after the node.
	rest := tag[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return
	}
	beacon := rest[slash+1:]
	if slash := strings.IndexByte(beacon, '/'); slash >= 0 {
		beacon = beacon[:slash]
	}
	if beacon == "" {
		return
	}
	s.m().beaconEvents.With(beacon).Inc()
}

// countDispatch records a job going out.
//
// The outstanding gauge counts nodes rather than jobs, because the
// question it answers is "how many machines have not answered", which
// is the one an operator asks during a partial outage.
func (s *Server) countDispatch(j *job.Job, matched int) {
	m := s.m()
	m.jobsDispatched.With(j.Fun).Inc()
	m.jobsOutstanding.Add(float64(matched))
}

// countReturn records a return arriving, and the states inside it.
//
// A state run's individual results are in the return, so the hub counts
// them from what it already has rather than asking the node for a
// metric it would have to keep.
func (s *Server) countReturn(ret *job.Return) {
	m := s.m()
	result := "failed"
	if ret.Success {
		result = "succeeded"
	}
	m.jobReturns.With(result).Inc()
	m.jobsOutstanding.Add(-1)
	if ret.DurationMS > 0 {
		observeSeconds(m.jobDuration.With(ret.Fun), time.Duration(ret.DurationMS)*time.Millisecond)
	}
	s.countStates(ret)
}

// countStates reads the per-state results out of a state run's return.
//
// SPEC 26.2 asks for `halite_state_states_total{result}` and
// `halite_state_changes_total`, and the hub already holds the only
// thing that knows them: the return itself. Anything that is not a
// state run, or a return whose shape is not the one SPEC 11.8 defines,
// is left alone rather than guessed at.
func (s *Server) countStates(ret *job.Return) {
	if !strings.HasPrefix(ret.Fun, "state.") || len(ret.Return) == 0 {
		return
	}
	var states map[string]struct {
		Result  *bool          `json:"result"`
		Changes map[string]any `json:"changes"`
	}
	if err := json.Unmarshal(ret.Return, &states); err != nil {
		return
	}
	m := s.m()
	for _, st := range states {
		switch {
		case st.Result == nil:
			// SPEC 11.8's third value: a test run reports what would
			// change, and counting that as a failure would make every
			// `--test` look like an outage.
			m.stateStates.With("unchanged_test").Inc()
		case *st.Result:
			m.stateStates.With("succeeded").Inc()
		default:
			m.stateStates.With("failed").Inc()
		}
		if len(st.Changes) > 0 {
			m.stateChanges.Inc()
		}
	}
}

// countingWriter records what a handler answered, for the file server's
// per-code counter and its byte total.
type countingWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (w *countingWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *countingWriter) Write(b []byte) (int, error) {
	w.wrote = true
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush and ReadFrom pass through, because http.ServeContent uses both
// and a wrapper that hides them turns a sendfile into a copy.
func (w *countingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) countFileRequest(w *countingWriter) {
	m := s.m()
	// `roots` is the only backend this build serves; gitfs and s3fs
	// are phase 5, and the label is here so that adding one does not
	// change the shape of a metric an estate is already graphing.
	m.fileRequests.With("roots", strconv.Itoa(w.status)).Inc()
	m.fileBytes.Add(float64(w.bytes))
}
