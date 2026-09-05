package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/transport"

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

	beaconEvents  *metrics.Counter
	beaconDropped *metrics.Counter

	authzDecisions *metrics.Counter

	requests        *metrics.Counter
	requestDuration *metrics.Histogram

	enrollments *metrics.Counter

	subscriberLag *metrics.Histogram

	gitfsFetch      *metrics.Histogram
	gitfsSignatures *metrics.Counter
	gitfsRefusals   *metrics.Counter

	orchRuns     *metrics.Counter
	stateCompile *metrics.Histogram
	stateRun     *metrics.Histogram
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
		// The other half of the pair SPEC 26.2 names. A node's beacon
		// queue is bounded and reports its overflow as an event, so
		// the hub can count what was lost without the node holding a
		// metric it has nowhere to expose.
		beaconDropped: r.Counter("halite_beacon_dropped_total",
			"Beacon events a node's bounded queue discarded, by beacon.", "beacon"),

		authzDecisions: r.Counter("halite_authz_decisions_total",
			"Authorization decisions, by outcome.", "result"),

		// The hub's own service metrics. The API has had these since
		// it was written and the hub had none, so "the API is slow"
		// could not be told from "the hub the API is waiting on is
		// slow" -- which is the first question asked and the one the
		// exposition could not answer.
		requests: r.Counter("halite_hub_requests_total",
			"Requests the hub answered, by route and status code.", "route", "code"),
		requestDuration: r.Histogram("halite_hub_request_duration_seconds",
			"Time to answer one request, by route.", nil, "route"),

		enrollments: r.Counter("halite_hub_enrollments_total",
			"Enrollment requests, by what the hub did with one.", "result"),

		subscriberLag: r.Histogram("halite_event_subscriber_lag_seconds",
			"How old an event was when a subscriber was handed it.",
			[]float64{0.01, 0.05, 0.25, 1, 5, 30, 120, 600}),

		gitfsFetch: r.Histogram("halite_gitfs_fetch_duration_seconds",
			"Time to fetch and materialise every git remote.", nil),
		gitfsSignatures: r.Counter("halite_gitfs_signature_failures_total",
			"Refs not served because their signature did not verify."),
		gitfsRefusals: r.Counter("halite_gitfs_refusals_total",
			"Refs not served, by why. Signature failures are counted here too.", "reason"),

		orchRuns: r.Counter("halite_orch_runs_total",
			"Orchestrations run on the hub, by outcome.", "result"),
		stateCompile: r.Histogram("halite_state_compile_duration_seconds",
			"Time to compile a state tree into a low state. On the hub this is orchestration.", nil),
		// End to end, because that is all a return carries: the node
		// reports one duration for the job, and compiling the tree
		// happened inside it. A node serving its own exposition splits
		// the two, and the same family there is the apply alone -- so
		// the hub's number is the larger by the compile time.
		stateRun: r.Histogram("halite_state_run_duration_seconds",
			"Time a node spent on a state run end to end, from its return. Compiling the tree is inside it.", nil),
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
	// Certificates expire, and an estate discovers that all at once
	// when a batch issued on the same afternoon a year ago stops
	// authenticating. `keys_accepted` does not fall until the record
	// is removed, so it says nothing about this.
	r.GaugeFunc("halite_hub_keys_expired",
		"Accepted nodes whose certificate has already expired.", func() float64 {
			return float64(s.countExpiring(0))
		})
	r.GaugeFunc("halite_hub_keys_expiring",
		"Accepted nodes whose certificate expires within thirty days.", func() float64 {
			return float64(s.countExpiring(30 * 24 * time.Hour))
		})
	r.GaugeFunc("halite_hub_soonest_certificate_expiry_seconds",
		"Seconds until the first node certificate expires. Zero when none is known.",
		func() float64 { return s.soonestNodeExpiry() })
	r.GaugeFunc("halite_hub_ca_expiry_seconds",
		"Seconds until the enrollment CA expires. Every node's identity is signed by it.",
		func() float64 { return s.caExpiry() })
}

// countKeys counts the key store records in one state.
func (s *Server) countKeys(want keystore.State) int {
	n := 0
	for _, rec := range s.keyRecords() {
		if rec.State == want {
			n++
		}
	}
	return n
}

// countExpiring counts accepted records whose certificate runs out
// within the window. A zero window counts the ones already expired.
func (s *Server) countExpiring(within time.Duration) int {
	return countExpiring(s.keyRecords(), s.now(), within)
}

// soonestNodeExpiry is how long the estate has before the first node
// certificate runs out.
func (s *Server) soonestNodeExpiry() float64 {
	return soonestExpiry(s.keyRecords(), s.now())
}

// countExpiring is the arithmetic, apart from the store, so that the
// boundary between "expired" and "expiring" is testable without one.
//
// Already expired is not "expiring": it is counted by the other gauge,
// and adding it here would make a fixed problem look like a growing
// one.
func countExpiring(records []*keystore.Record, now time.Time, within time.Duration) int {
	n := 0
	for _, rec := range records {
		if rec.State != keystore.Accepted || rec.NotAfter.IsZero() {
			continue
		}
		expired := !rec.NotAfter.After(now)
		if within == 0 {
			if expired {
				n++
			}
			continue
		}
		if !expired && !rec.NotAfter.After(now.Add(within)) {
			n++
		}
	}
	return n
}

// soonestExpiry is when the first accepted certificate runs out, in
// seconds from now. Zero when there is none; negative when one has
// already gone, which is the honest answer rather than a floor at zero.
func soonestExpiry(records []*keystore.Record, now time.Time) float64 {
	soonest := time.Time{}
	for _, rec := range records {
		if rec.State != keystore.Accepted || rec.NotAfter.IsZero() {
			continue
		}
		if soonest.IsZero() || rec.NotAfter.Before(soonest) {
			soonest = rec.NotAfter
		}
	}
	if soonest.IsZero() {
		return 0
	}
	return soonest.Sub(now).Seconds()
}

// caExpiry is how long the enrollment CA has left. A CA that expires
// takes every node with it, and it is the one certificate nobody
// renews on a timer.
func (s *Server) caExpiry() float64 {
	if s.Authority == nil || s.Authority.CA == nil || s.Authority.CA.Cert == nil {
		return 0
	}
	return s.Authority.CA.Cert.NotAfter.Sub(s.now()).Seconds()
}

// keyRecords is the key store's contents, or nothing when it cannot be
// read. A gauge that cannot answer reports zero rather than stopping
// the scrape.
func (s *Server) keyRecords() []*keystore.Record {
	if s.Authority == nil || s.Authority.Store == nil {
		return nil
	}
	records, err := s.Authority.Store.List()
	if err != nil {
		return nil
	}
	return records
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
func (s *Server) countBeaconEvent(tag string, data map[string]any) {
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
	suffix := ""
	if slash := strings.IndexByte(beacon, '/'); slash >= 0 {
		beacon, suffix = beacon[:slash], beacon[slash+1:]
	}
	if beacon == "" {
		return
	}
	s.m().beaconEvents.With(beacon).Inc()
	if suffix == beaconOverflowSuffix {
		s.countBeaconDrops(beacon, data)
	}
}

// beaconOverflowSuffix is what a node tags the event it sends when its
// bounded beacon queue discarded something. SPEC 16.3 requires the loss
// be reported; this is the hub turning that report into a number.
const beaconOverflowSuffix = "overflow"

// countBeaconDrops reads the count out of an overflow event.
//
// The payload has been through JSON, so the number arrives as a float
// whatever the node sent. A payload that does not carry one is counted
// as a single drop rather than as none: the event exists only because
// something was lost.
func (s *Server) countBeaconDrops(beacon string, data map[string]any) {
	dropped := 1.0
	switch n := data["dropped"].(type) {
	case float64:
		dropped = n
	case int64:
		dropped = float64(n)
	case int:
		dropped = float64(n)
	case json.Number:
		if parsed, err := n.Float64(); err == nil {
			dropped = parsed
		}
	}
	if dropped <= 0 {
		return
	}
	s.m().beaconDropped.With(beacon).Add(dropped)
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
		took := time.Duration(ret.DurationMS) * time.Millisecond
		observeSeconds(m.jobDuration.With(ret.Fun), took)
		// SPEC 26.2's `halite_state_run_duration_seconds`. The node
		// already reports how long it spent, so the hub separates state
		// runs out of the general job timing rather than asking for a
		// second number -- a highstate that is getting slower is not
		// visible in a distribution shared with `test.ping`.
		if strings.HasPrefix(ret.Fun, "state.") {
			observeSeconds(m.stateRun, took)
		}
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

// measured counts and times every request the hub answers.
//
// A wrapper around the whole mux rather than per handler, so that a
// route added later is counted without anybody remembering to: an
// endpoint that is missing from the exposition looks like an endpoint
// nobody calls.
func (s *Server) measured(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := s.now()
		rec := &countingWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		m := s.m()
		route := hubRoute(r.URL.Path)
		m.requests.With(route, strconv.Itoa(rec.status)).Inc()
		if elapsed := s.now().Sub(started); elapsed >= 0 {
			m.requestDuration.With(route).Observe(elapsed.Seconds())
		}
	})
}

// hubRoute names the route a path belongs to, with the variable part
// removed.
//
// A series per job identifier or per file served is the unbounded label
// the registry's own cap exists to survive, and on the file server it
// would be one series per file in the tree.
func hubRoute(path string) string {
	if rest, ok := strings.CutPrefix(path, transport.PathFiles); ok && rest != "" {
		return transport.PathFiles + "{path}"
	}
	if rest, ok := strings.CutPrefix(path, transport.PathJob); ok && rest != "" {
		// `/v1/jobs/{jid}/kill` and `/v1/jobs/{jid}` are different
		// routes; the identifier between them is not.
		if _, tail, found := strings.Cut(rest, "/"); found && tail != "" {
			return transport.PathJob + "{jid}/" + tail
		}
		return transport.PathJob + "{jid}"
	}
	return path
}

// countEnrollment records what happened to an enrollment request.
func (s *Server) countEnrollment(result string) {
	s.m().enrollments.With(result).Inc()
}

// observeSubscriberLag records how far behind the bus a subscriber was
// when it was handed one event.
//
// Measured at delivery rather than as a queue depth, because the
// question is "is anything watching the bus keeping up", and a
// subscriber that reconnects from an old offset answers it directly.
// An event with no stamp, and one stamped in the future by a node whose
// clock is wrong, are both left out rather than recorded as a negative
// lag.
func (s *Server) observeSubscriberLag(stamp time.Time) {
	if stamp.IsZero() {
		return
	}
	if lag := s.now().Sub(stamp); lag >= 0 {
		s.m().subscriberLag.Observe(lag.Seconds())
	}
}

// ObserveGitFetch records one pass over every git remote.
//
// Exported because the git backend is assembled by the command rather
// than by the server: the hub owns the registry and the command owns
// the fetch, and this is the seam between them.
func (s *Server) ObserveGitFetch(took time.Duration, refusals map[string]int) {
	m := s.m()
	if took >= 0 {
		m.gitfsFetch.Observe(took.Seconds())
	}
	for reason, n := range refusals {
		m.gitfsRefusals.With(reason).Add(float64(n))
		if reason == "signature" {
			// SPEC 13.3 makes an unverified ref one that is not
			// served, which is a control rather than a warning. A
			// control needs a number behind it.
			m.gitfsSignatures.Add(float64(n))
		}
	}
}

// countOrchestration records an orchestration and how long compiling it
// took.
func (s *Server) countOrchestration(result string, compile time.Duration) {
	m := s.m()
	m.orchRuns.With(result).Inc()
	if compile >= 0 {
		m.stateCompile.Observe(compile.Seconds())
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

// ReadFrom is what makes the file server's sendfile survive the
// wrapper.
//
// The comment above said this passed through and it did not: only Flush
// was here. It mattered little while the wrapper was on the file server
// alone and matters more now that every response goes through one, so
// the claim and the code agree here rather than one of them being
// right.
func (w *countingWriter) ReadFrom(r io.Reader) (int64, error) {
	w.wrote = true
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(r)
		w.bytes += n
		return n, err
	}
	// No sendfile underneath: copy through Write, which counts.
	n, err := io.Copy(struct{ io.Writer }{w.ResponseWriter}, r)
	w.bytes += n
	return n, err
}

func (s *Server) countFileRequest(w *countingWriter) {
	m := s.m()
	// `roots` is the only backend this build serves; gitfs and s3fs
	// are phase 5, and the label is here so that adding one does not
	// change the shape of a metric an estate is already graphing.
	m.fileRequests.With("roots", strconv.Itoa(w.status)).Inc()
	m.fileBytes.Add(float64(w.bytes))
}
