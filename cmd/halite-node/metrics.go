package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/metrics"
)

// nodeMetrics is what a node counts about itself.
//
// A node has no listener, by design: SPEC 6.1 has it dial the hub and
// be dialled by nothing, and opening a scrape port on every managed
// machine to answer a question about the control plane is a larger
// change to an estate than the answer is worth. So the exposition goes
// to a file, which is how node_exporter's textfile collector has always
// taken metrics from things that cannot serve them, and the scraper
// that already reaches every machine picks it up with the node's own
// `instance` label already on it.
//
// Only the agent writes it. A one-shot `halite-node call` is a fresh
// process whose counters start at zero, and writing those over the
// agent's file would report every counter in the estate falling to
// nearly nothing each time an operator ran a command by hand -- which
// a scraper reads as a restart, not as a mistake.
type nodeMetrics struct {
	registry *metrics.Registry
	path     string
	interval time.Duration
	mode     os.FileMode

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
// A node with no `metrics_textfile` gets a struct whose registry is
// nil, and every metric on it is nil too. The metrics package makes
// each of those a no-op, so the call sites are unconditional and a node
// that exposes nothing pays for nothing.
func newNodeMetrics(cfg *config.Config) *nodeMetrics {
	m := &nodeMetrics{}
	if !cfg.Bool("metrics", true) {
		return m
	}
	m.path = strings.TrimSpace(cfg.String("metrics_textfile", ""))
	if m.path == "" {
		return m
	}
	m.interval = cfg.Duration("metrics_interval", time.Minute)
	if m.interval <= 0 {
		m.interval = time.Minute
	}
	// World-readable, because the collector runs as its own account and
	// this is an exposition rather than a secret. The redactor never
	// sees a metric: nothing here carries a value from pillar.
	m.mode = 0o644

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

// write renders the exposition and replaces the file.
//
// By rename, because the collector reads whenever it likes: a partial
// file is one it rejects whole, and it reports that as the node's
// metrics being broken rather than as a write in progress.
func (m *nodeMetrics) write() error {
	if !m.on() {
		return nil
	}
	var body strings.Builder
	if err := m.registry.Write(&body); err != nil {
		return err
	}
	if dir := filepath.Dir(m.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return atomicfile.Write(m.path, []byte(body.String()), m.mode)
}

// run rewrites the file on the interval until the context ends.
//
// The write on the way out is the caller's, through Report, and not
// this loop's: this runs in a goroutine, and a process that returns
// from its main function does not wait for one. The last write was
// here at first and raced the exit -- it happened when the scheduler
// felt like it, which is the worst kind of "usually works".
func (m *nodeMetrics) run(ctx context.Context, onError func(error)) {
	if !m.on() {
		return
	}
	m.Report(onError)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.Report(onError)
		case <-ctx.Done():
			return
		}
	}
}

// Report writes the file and hands any failure to the caller.
//
// Called once more as the node stops, on the way out of the agent's own
// goroutine: without it a node stopped for a deployment leaves up to an
// interval of counting in memory, and the counters come back from zero
// on the other side with nothing to say the gap was a restart rather
// than an idle minute.
func (m *nodeMetrics) Report(onError func(error)) {
	if err := m.write(); err != nil && onError != nil {
		onError(err)
	}
}
