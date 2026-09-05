package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/metrics"
)

// nodeMetrics is what a node counts about itself.
//
// SPEC 6.1 has a node dial the hub and be dialled by nothing, and this
// is the one deliberate exception: an agent asked for `metrics_listen`
// serves `/v1/metrics` there and nothing else. Recorded as DIVERGENCE
// 1.11, because it is a listener on a machine the specification says
// has none.
//
// Off unless the address is set. A port on every managed machine is a
// decision an operator makes, not one that arrives with an upgrade.
//
// Only the agent serves it. A one-shot `halite-node call` is a fresh
// process whose counters start at zero and whose lifetime is a second;
// there is nothing for a scraper to reach and nothing worth reaching.
type nodeMetrics struct {
	registry *metrics.Registry
	listen   string
	// The certificate settings are kept rather than loaded, because
	// every command line builds one of these and only the agent serves
	// anything: reading a certificate from disk for `halite-node call`
	// would report a misconfigured endpoint on every command an
	// operator typed, and reading it at construction would report it
	// through whatever logger existed then.
	certFile, keyFile, clientCAFile string

	jobs         *metrics.Counter
	jobDuration  *metrics.Histogram
	jobsRefused  *metrics.Counter
	returnsDrop  *metrics.Counter
	stateCompile *metrics.Histogram
	stateRun     *metrics.Histogram

	extCalls    *metrics.Counter
	extDuration *metrics.Histogram
	extTimeouts *metrics.Counter

	beaconEvents      *metrics.Counter
	beaconDropped     *metrics.Counter
	beaconRateLimited *metrics.Counter
	beaconFailures    *metrics.Counter

	hubRequests *metrics.Counter
	hubDuration *metrics.Histogram
	reconnects  *metrics.Counter
	connected   *metrics.Gauge

	scheduleRuns *metrics.Counter
}

// newNodeMetrics reads the settings and declares the families.
//
// A node with no `metrics_listen` gets a struct whose registry is nil,
// and every metric on it is nil too. The metrics package makes each of
// those a no-op, so the call sites are unconditional and a node that
// exposes nothing pays for nothing.
//
// Nothing is read from disk here and nothing can fail. What the
// certificate settings name is checked when the agent comes to serve
// them, which is where the failure is worth a line and where there is a
// logger that has the node's identity on it.
func newNodeMetrics(cfg *config.Config) *nodeMetrics {
	m := &nodeMetrics{}
	if !cfg.Bool("metrics", true) {
		return m
	}
	m.listen = strings.TrimSpace(cfg.String("metrics_listen", ""))
	if m.listen == "" {
		return m
	}
	m.certFile = cfg.String("metrics_tls_cert", "")
	m.keyFile = cfg.String("metrics_tls_key", "")
	m.clientCAFile = cfg.String("metrics_client_ca", "")

	r := metrics.NewRegistry()
	m.registry = r
	r.BuildInfo("node")

	m.jobs = r.Counter("halite_node_jobs_total",
		"Jobs this node ran, by function and outcome.", "fun", "result")
	m.jobDuration = r.Histogram("halite_node_job_duration_seconds",
		"Time this node spent on one job, by function.", nil, "fun")
	m.jobsRefused = r.Counter("halite_node_jobs_refused_total",
		"Jobs this node would not run, by why. SPEC 6.3's structured refusals.", "reason")
	m.returnsDrop = r.Counter("halite_node_returns_dropped_total",
		"Returns discarded because the queue to the hub was full.")

	m.stateCompile = r.Histogram("halite_state_compile_duration_seconds",
		"Time to compile this node's high state into a low state.", nil)
	// The apply alone, not the whole job: the compile above is the
	// other half, and splitting them is the reason for recording this
	// here at all. The hub's family of the same name is end to end,
	// because a return carries one duration and nothing finer.
	m.stateRun = r.Histogram("halite_state_run_duration_seconds",
		"Time to apply one compiled low state, not counting compiling it.", nil)

	m.extCalls = r.Counter("halite_ext_invocations_total",
		"Extension calls, by extension and outcome.", "name", "result")
	m.extDuration = r.Histogram("halite_ext_duration_seconds",
		"Time one extension call took.", nil, "name")
	m.extTimeouts = r.Counter("halite_ext_timeouts_total",
		"Extension calls that ran out of time.", "name")

	m.beaconEvents = r.Counter("halite_beacon_events_total",
		"Beacon events this node produced, by beacon.", "beacon")
	m.beaconDropped = r.Counter("halite_beacon_dropped_total",
		"Beacon events the bounded queue discarded to make room, by beacon.", "beacon")
	m.beaconRateLimited = r.Counter("halite_beacon_rate_limited_total",
		"Beacon events the rate limit refused, by beacon.", "beacon")
	m.beaconFailures = r.Counter("halite_beacon_failures_total",
		"Beacon polls that failed, by beacon.", "beacon")

	m.hubRequests = r.Counter("halite_node_hub_requests_total",
		"Requests this node made to its hub, by route and status. A zero status is a request that got none.",
		"route", "code")
	m.hubDuration = r.Histogram("halite_node_hub_request_duration_seconds",
		"Time one request to the hub took, by route.", nil, "route")
	m.reconnects = r.Counter("halite_node_hub_reconnects_total",
		"Times the subscribe stream was opened. The first connection counts.")
	m.connected = r.Gauge("halite_node_connected",
		"1 while the subscribe stream to the hub is open.")

	m.scheduleRuns = r.Counter("halite_node_schedule_runs_total",
		"Scheduled jobs this node started, by schedule entry.", "name")

	return m
}

// on reports whether anything is being recorded.
func (m *nodeMetrics) on() bool { return m != nil && m.registry != nil }

// countJob records one finished job.
func (m *nodeMetrics) countJob(ret *job.Return, took time.Duration) {
	if !m.on() {
		return
	}
	result := "failed"
	if ret.Success {
		result = "succeeded"
	}
	m.jobs.With(ret.Fun, result).Inc()
	if took >= 0 {
		m.jobDuration.With(ret.Fun).Observe(took.Seconds())
	}
}

// countRefusal records a job the node would not run.
//
// The reason comes from the structured refusal rather than from the
// message, so `replayed` stays `replayed` when somebody rewords it.
func (m *nodeMetrics) countRefusal(err error) {
	if !m.on() {
		return
	}
	reason := "other"
	var refusal *job.Refusal
	if errors.As(err, &refusal) {
		reason = refusal.Reason
	}
	m.jobsRefused.With(reason).Inc()
}

// countDroppedReturn records a return the queue to the hub could not
// hold. It is a drop path, so SPEC 26.2 requires a counter behind it.
func (m *nodeMetrics) countDroppedReturn() {
	if m.on() {
		m.returnsDrop.Inc()
	}
}

// countConnect records an attempt to open the subscribe stream.
//
// The gauge goes up here rather than when the hub answers, because a
// node that is dialling is a node that believes it is connected; the
// disconnect that follows a refused dial puts it back.
func (m *nodeMetrics) countConnect() {
	if m.on() {
		m.reconnects.Inc()
		m.connected.Set(1)
	}
}

// countDisconnect records the stream ending, for whatever reason.
func (m *nodeMetrics) countDisconnect() {
	if m.on() {
		m.connected.Set(0)
	}
}

// countScheduleRun records a scheduled job starting.
func (m *nodeMetrics) countScheduleRun(name string) {
	if m.on() {
		m.scheduleRuns.With(name).Inc()
	}
}

// observeStateCompile records how long turning a tree into a low state
// took. SPEC 26.2's `halite_state_compile_duration_seconds`.
func (m *nodeMetrics) observeStateCompile(took time.Duration) {
	if m.on() && took >= 0 {
		m.stateCompile.Observe(took.Seconds())
	}
}

// observeStateRun records how long applying one took.
func (m *nodeMetrics) observeStateRun(took time.Duration) {
	if m.on() && took >= 0 {
		m.stateRun.Observe(took.Seconds())
	}
}

// observeExtension is the hook the extension runtime calls.
func (m *nodeMetrics) observeExtension(name, result string, took time.Duration) {
	if !m.on() {
		return
	}
	m.extCalls.With(name, result).Inc()
	if took >= 0 {
		m.extDuration.With(name).Observe(took.Seconds())
	}
	if result == "timed_out" {
		m.extTimeouts.With(name).Inc()
	}
}

// observeBeacon is the hook the beacon engine calls.
func (m *nodeMetrics) observeBeacon(event, beaconName string, n int) {
	if !m.on() {
		return
	}
	switch event {
	case "fired":
		m.beaconEvents.With(beaconName).Add(float64(n))
	case "dropped":
		m.beaconDropped.With(beaconName).Add(float64(n))
	case "rate_limited":
		m.beaconRateLimited.With(beaconName).Add(float64(n))
	case "failed":
		m.beaconFailures.With(beaconName).Add(float64(n))
	}
}

// observeHubRequest is the hook transport.Client calls.
func (m *nodeMetrics) observeHubRequest(route string, status int, took time.Duration) {
	if !m.on() {
		return
	}
	m.hubRequests.With(route, strconv.Itoa(status)).Inc()
	if took >= 0 {
		m.hubDuration.With(route).Observe(took.Seconds())
	}
}

// gauge registers a value read at scrape time rather than stored.
func (m *nodeMetrics) gauge(name, help string, read func() float64) {
	if !m.on() {
		return
	}
	m.registry.GaugeFunc(name, help, read)
}

// requiresClientCert reports whether a scraper has to present one, for
// the line the agent logs when it starts: "serving metrics" and
// "serving metrics to anybody who can reach the port" are different
// facts, and only one of them is in the configuration file.
func (m *nodeMetrics) requiresClientCert() bool { return m.clientCAFile != "" }

// metricsTLS builds the listener's TLS configuration.
//
// There is no plaintext mode. A node's exposition says which functions
// ran, which extensions, and when a deployment went out, and that is
// the argument that put the hub's endpoint behind a certificate and the
// API's behind a token; a node's is not less telling because it is one
// machine.
//
// The node's own `node.crt` is deliberately not used. It is issued for
// client authentication and carries no DNS or IP name, so a scraper has
// nothing to verify it against and Go refuses it as a serving
// certificate. This is a certificate the operator supplies, the way
// `halite-api` takes `tls_cert`.
func (m *nodeMetrics) serverTLS() (*tls.Config, error) {
	if m.certFile == "" || m.keyFile == "" {
		return nil, fmt.Errorf("metrics_listen is set to %q and metrics_tls_cert "+
			"and metrics_tls_key are not; there is no plaintext metrics endpoint",
			m.listen)
	}
	pair, err := tls.LoadX509KeyPair(m.certFile, m.keyFile)
	if err != nil {
		return nil, fmt.Errorf("the metrics certificate: %w", err)
	}
	out := &tls.Config{
		Certificates: []tls.Certificate{pair},
		// 1.3 only, as everywhere else here: what talks to this is a
		// scraper, so there is nothing to be compatible with.
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h2", "http/1.1"},
	}

	// No ALPN gate, unlike the hub's port. The whole point of this
	// endpoint is that an ordinary Prometheus can reach it, and the hub
	// is unscrapeable precisely because it has one.
	if m.clientCAFile == "" {
		return out, nil
	}
	raw, err := os.ReadFile(m.clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("the metrics client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("%s holds no certificate this can verify a scraper against",
			m.clientCAFile)
	}
	out.ClientCAs = pool
	out.ClientAuth = tls.RequireAndVerifyClientCert
	return out, nil
}

// handler answers `/v1/metrics` and nothing else.
//
// Nothing else on purpose: this is a scrape target, not a second
// control surface on a managed machine. An unrouted path gets a 404
// rather than a description of what else might be here.
func (m *nodeMetrics) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		var body strings.Builder
		if err := m.registry.Write(&body); err != nil {
			http.Error(w, "the exposition could not be written", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", metrics.ContentType)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body.String())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "this node serves /v1/metrics and nothing else", http.StatusNotFound)
	})
	return mux
}

// serve runs the endpoint until the context ends.
//
// The listener is opened here rather than at construction so that the
// address being in use is reported when the agent starts, next to
// everything else it says about itself, and reported rather than fatal:
// a node that will not run because a port is taken is a node no
// highstate can reach to free the port.
func (m *nodeMetrics) serve(ctx context.Context, onError func(error), started func(string)) {
	if !m.on() || m.listen == "" {
		return
	}
	serverTLS, err := m.serverTLS()
	if err != nil {
		if onError != nil {
			onError(err)
		}
		return
	}
	ln, err := tls.Listen("tcp", m.listen, serverTLS)
	if err != nil {
		if onError != nil {
			onError(fmt.Errorf("the metrics endpoint could not listen on %s: %w", m.listen, err))
		}
		return
	}
	if started != nil {
		started(ln.Addr().String())
	}
	srv := &http.Server{
		Handler:           m.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && onError != nil {
		onError(fmt.Errorf("the metrics endpoint stopped: %w", err))
	}
}
