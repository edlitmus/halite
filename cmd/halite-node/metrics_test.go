package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/job"
)

// nodeMetricsFor builds the recorder a configuration asks for.
func nodeMetricsFor(t *testing.T, body string) *nodeMetrics {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(config.Node, config.LoadOptions{
		Path:         writeConfig(t, dir, body),
		AllowMissing: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return newNodeMetrics(cfg)
}

// A node has no listener, so its exposition goes to the file the
// textfile collector reads. Without the setting it keeps nothing at
// all: recording numbers nobody can read is a cost with no reader.
func TestANodeRecordsNothingWithoutTheSetting(t *testing.T) {
	m := nodeMetricsFor(t, "id: web1.example\n")
	if m.on() {
		t.Fatal("a node with no metrics_textfile is recording")
	}
	// Every call site is unconditional, so all of them have to be safe
	// on a recorder that is off.
	m.countJob(&job.Return{Fun: "test.ping", Success: true}, time.Second)
	m.countRefusal(ErrQueueFull)
	m.observeExtension("thing", "succeeded", time.Second)
	m.observeBeacon("dropped", "diskusage", 3)
	m.observeHubRequest("/v1/return", 200, time.Second)
	m.observeStateCompile(time.Second)
	m.observeStateRun(time.Second)
	m.countDroppedReturn()
	m.countConnect()
	m.countDisconnect()
	m.countScheduleRun("nightly")
	m.gauge("halite_node_job_queue_depth", "unused", func() float64 { return 1 })
	if err := m.write(); err != nil {
		t.Fatalf("writing a recorder that is off: %v", err)
	}
}

// `metrics: false` turns it off even with a path set, the same way it
// does on the hub and the API.
func TestMetricsFalseTurnsANodeOffWithAPathSet(t *testing.T) {
	dir := t.TempDir()
	m := nodeMetricsFor(t, "metrics: false\nmetrics_textfile: "+
		filepath.Join(dir, "halite.prom")+"\n")
	if m.on() {
		t.Fatal("metrics: false left the node recording")
	}
}

// What the collector actually reads.
func TestTheNodeWritesAnExpositionTheCollectorCanRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "collector", "halite.prom")
	m := nodeMetricsFor(t, "metrics_textfile: "+path+"\n")
	if !m.on() {
		t.Fatal("a node with metrics_textfile is not recording")
	}

	m.countJob(&job.Return{Fun: "state.apply", Success: true}, 12*time.Second)
	m.countJob(&job.Return{Fun: "state.apply", Success: false}, time.Second)
	m.countRefusal(&job.Refusal{Reason: job.ReasonReplayed})
	m.countDroppedReturn()
	m.observeStateCompile(400 * time.Millisecond)
	m.observeStateRun(11 * time.Second)
	m.observeExtension("inventory", "timed_out", 60*time.Second)
	m.observeBeacon("dropped", "diskusage", 7)
	m.observeHubRequest("/v1/return", 200, 30*time.Millisecond)
	m.countConnect()

	if err := m.write(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the directory the collector reads was not created: %v", err)
	}
	body := string(raw)

	for _, want := range []string{
		`halite_build_info{component="node"`,
		`halite_node_jobs_total{fun="state.apply",result="succeeded"} 1`,
		`halite_node_jobs_total{fun="state.apply",result="failed"} 1`,
		`halite_node_jobs_refused_total{reason="replayed"} 1`,
		"halite_node_returns_dropped_total 1",
		"halite_state_compile_duration_seconds_count 1",
		"halite_state_run_duration_seconds_sum 11",
		`halite_ext_invocations_total{name="inventory",result="timed_out"} 1`,
		`halite_ext_timeouts_total{name="inventory"} 1`,
		`halite_beacon_dropped_total{beacon="diskusage"} 7`,
		`halite_node_hub_requests_total{route="/v1/return",code="200"} 1`,
		"halite_node_connected 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the exposition is missing %q", want)
		}
	}
	checkExposition(t, body)
}

// A file the collector rejects is worse than no file: it reports the
// node's metrics as broken rather than the node as quiet. The format
// allows one declaration per family, and every sample line has to be a
// name and a number.
func checkExposition(t *testing.T, body string) {
	t.Helper()
	declared := map[string]int{}
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "# HELP "), " ")
			declared[name]++
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		_, value, found := strings.Cut(line, " ")
		if !found {
			t.Errorf("this line carries no value: %q", line)
			continue
		}
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			switch value {
			case "+Inf", "-Inf", "NaN":
			default:
				t.Errorf("this line's value is not a number: %q", line)
			}
		}
	}
	for name, n := range declared {
		if n > 1 {
			t.Errorf("%s is declared %d times; a scraper rejects the whole body for that", name, n)
		}
	}
	if len(declared) == 0 {
		t.Error("the exposition declared nothing")
	}
}

// A refusal's reason is the structured token, not the message. A metric
// keyed off the wording stops working the day somebody rewords it.
func TestARefusalIsCountedByItsReasonAndNotItsMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "halite.prom")
	m := nodeMetricsFor(t, "metrics_textfile: "+path+"\n")

	m.countRefusal(&job.Refusal{Reason: job.ReasonExpired, Detail: "by 4m"})
	m.countRefusal(ErrQueueFull)

	if err := m.write(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, `halite_node_jobs_refused_total{reason="expired"} 1`) {
		t.Errorf("a structured refusal was not counted by its reason:\n%s", body)
	}
	// Anything that is not one of SPEC 6.3's refusals is counted under
	// one label rather than under its message, which would be a series
	// per distinct error string.
	if !strings.Contains(body, `halite_node_jobs_refused_total{reason="other"} 1`) {
		t.Errorf("a full queue was not counted:\n%s", body)
	}
}

// The file is replaced, not appended to or truncated in place: the
// collector reads whenever it likes, and half a file is one it rejects
// whole.
func TestARewriteReplacesTheWholeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "halite.prom")
	m := nodeMetricsFor(t, "metrics_textfile: "+path+"\n")

	m.countJob(&job.Return{Fun: "test.ping", Success: true}, time.Second)
	if err := m.write(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	m.countJob(&job.Return{Fun: "test.ping", Success: true}, time.Second)
	if err := m.write(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(second), `halite_node_jobs_total{fun="test.ping",result="succeeded"} 2`) {
		t.Errorf("the second write did not carry the new total:\n%s", second)
	}
	if len(second) < len(first)/2 {
		t.Errorf("the second write is %d bytes against %d; it looks truncated",
			len(second), len(first))
	}
	// No temporary file left behind for the collector to try to read.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the directory holds %v; the collector reads every file in it", names)
	}
	checkExposition(t, string(second))
}

// Through the real path, not the recorder alone: a hook that is never
// called is a counter that reads zero for ever, and zero is what a
// healthy node looks like.
func TestRunningAJobMovesTheNodesCounters(t *testing.T) {
	n := nodeWithBrokenPillar(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "halite.prom")
	n.metrics = nodeMetricsFor(t, "metrics_textfile: "+path+"\n")

	if ret := n.executeJob(&job.Job{JID: job.ID("20260904T1"), Fun: "test.ping"}); !ret.Success {
		t.Fatalf("test.ping failed: %s", ret.Return)
	}
	if ret := n.executeJob(&job.Job{JID: job.ID("20260904T2"), Fun: "nosuch.function"}); ret.Success {
		t.Fatal("a function this build does not ship reported success")
	}

	if err := n.metrics.write(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`halite_node_jobs_total{fun="test.ping",result="succeeded"} 1`,
		`halite_node_jobs_total{fun="nosuch.function",result="failed"} 1`,
		`halite_node_job_duration_seconds_count{fun="test.ping"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the exposition is missing %q:\n%s", want, body)
		}
	}
}

// A full queue is a drop path, and SPEC 26.2 wants a number behind every
// one of them. It was a refusal nothing counted.
func TestAFullJobQueueIsCounted(t *testing.T) {
	n := nodeWithBrokenPillar(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "halite.prom")
	n.metrics = nodeMetricsFor(t, "metrics_textfile: "+path+"\n")

	e := newExecutor(n, 1, func(*job.Return) {})
	if err := e.Offer(runnableJob(t, "20260904T101010101010")); err != nil {
		t.Fatal(err)
	}
	if err := e.Offer(runnableJob(t, "20260904T101010101011")); err == nil {
		t.Fatal("the queue took a second job with a depth of one")
	}
	if got := e.Depth(); got != 1 {
		t.Errorf("depth = %d, want 1", got)
	}

	if err := n.metrics.write(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `halite_node_jobs_refused_total{reason="other"} 1`) {
		t.Errorf("a refused job was not counted:\n%s", raw)
	}
}

// runnableJob is a job the replay guard admits, so that a test about
// the queue is about the queue.
func runnableJob(t *testing.T, jid string) *job.Job {
	t.Helper()
	nonce, err := job.Nonce()
	if err != nil {
		t.Fatal(err)
	}
	return &job.Job{
		JID:     job.ID(jid),
		Fun:     "test.ping",
		Nonce:   nonce,
		Expires: time.Now().Add(job.DefaultTTL),
	}
}

// A path the node cannot use is reported at startup, not discovered an
// interval later and not fatal. A node that refused to start over its
// metrics file would be one no highstate could reach to fix.
func TestAnUnusableMetricsPathIsReportedAndNotFatal(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "a-file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A path whose parent is a file, which no MkdirAll can create.
	m := nodeMetricsFor(t, "metrics_textfile: "+filepath.Join(blocked, "halite.prom")+"\n")
	if !m.on() {
		t.Fatal("the recorder is off; this test would pass for the wrong reason")
	}
	err := m.write()
	if err == nil {
		t.Fatal("writing into a path that cannot exist reported success")
	}
	if !strings.Contains(err.Error(), blocked) {
		t.Errorf("the error does not name the path: %v", err)
	}
}

// The loop writes on the interval and stops when the context ends. It
// must not do the final write itself: it runs in a goroutine, and a
// process returning from main does not wait for one, so the write that
// matters most would land only when the scheduler allowed.
func TestTheIntervalLoopWritesAndTheCallerWritesLast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "halite.prom")
	m := nodeMetricsFor(t, "metrics_textfile: "+path+"\nmetrics_interval: 20ms\n")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.run(ctx, nil); close(done) }()

	// The first write happens immediately, so the file exists before
	// an interval has passed.
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no file was written at startup")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// Counted after the loop is running, and not written by it yet.
	m.countDroppedReturn()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the loop did not stop with the context")
	}

	// The caller's write is what carries it.
	m.Report(func(err error) { t.Fatalf("the last write failed: %v", err) })
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "halite_node_returns_dropped_total 1") {
		t.Errorf("the last write did not carry what was counted after the loop stopped:\n%s", raw)
	}
}
