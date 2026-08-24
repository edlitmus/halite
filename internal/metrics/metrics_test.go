package metrics

import (
	"strings"
	"sync"
	"testing"
)

func render(t *testing.T, r *Registry) string {
	t.Helper()
	var b strings.Builder
	if err := r.Write(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestACounterIsExposedWithItsHelpAndType(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("halite_jobs_dispatched_total", "Jobs dispatched.", "fun")
	c.With("test.ping").Inc()
	c.With("test.ping").Inc()
	c.With("state.apply").Add(3)

	out := render(t, r)
	for _, want := range []string{
		"# HELP halite_jobs_dispatched_total Jobs dispatched.",
		"# TYPE halite_jobs_dispatched_total counter",
		`halite_jobs_dispatched_total{fun="state.apply"} 3`,
		`halite_jobs_dispatched_total{fun="test.ping"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the exposition is missing %q:\n%s", want, out)
		}
	}
}

// A counter that has never been observed is written as zero rather than
// left out: absent and zero read very differently on a dashboard, and
// only one of them is what "nothing has gone wrong yet" means.
func TestAnUnobservedUnlabelledCounterIsZeroRatherThanAbsent(t *testing.T) {
	r := NewRegistry()
	r.Counter("halite_events_dropped_total", "Events dropped.")
	out := render(t, r)
	if !strings.Contains(out, "halite_events_dropped_total 0") {
		t.Errorf("an unobserved counter is not exposed:\n%s", out)
	}
}

func TestAGaugeGoesUpAndDown(t *testing.T) {
	r := NewRegistry()
	g := r.Gauge("halite_hub_nodes_connected", "Nodes connected.")
	g.Set(4)
	g.Add(-1)
	if out := render(t, r); !strings.Contains(out, "halite_hub_nodes_connected 3") {
		t.Errorf("the gauge reads wrong:\n%s", out)
	}
}

// A counter that went backwards is read by every consumer as a process
// restart, which would misreport an estate rather than a metric.
func TestACounterRefusesToGoBackwards(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("halite_authz_decisions_total", "Decisions.")
	c.Add(5)
	c.Add(-3)
	if out := render(t, r); !strings.Contains(out, "halite_authz_decisions_total 5") {
		t.Errorf("the counter moved backwards:\n%s", out)
	}
}

func TestAHistogramBucketsCumulatively(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("halite_job_duration_seconds", "Job duration.", []float64{0.1, 1, 10}, "fun")
	for _, v := range []float64{0.05, 0.5, 5, 50} {
		h.With("test.ping").Observe(v)
	}
	out := render(t, r)
	for _, want := range []string{
		`halite_job_duration_seconds_bucket{fun="test.ping",le="0.1"} 1`,
		`halite_job_duration_seconds_bucket{fun="test.ping",le="1"} 2`,
		`halite_job_duration_seconds_bucket{fun="test.ping",le="10"} 3`,
		`halite_job_duration_seconds_bucket{fun="test.ping",le="+Inf"} 4`,
		`halite_job_duration_seconds_sum{fun="test.ping"} 55.55`,
		`halite_job_duration_seconds_count{fun="test.ping"} 4`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the histogram is missing %q:\n%s", want, out)
		}
	}
}

// A label value carries a function name, an event tag, or a path, none
// of which this program chose. A quote would produce a line no scraper
// can parse; a newline would produce a line the sender chose.
func TestALabelValueCannotBreakOutOfItsLine(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("halite_events_published_total", "Events.", "tag_prefix")
	c.With("halite/\"odd\"\nhalite_injected 99").Inc()

	out := render(t, r)
	if strings.Contains(out, "\nhalite_injected") {
		t.Errorf("a label value produced its own line:\n%s", out)
	}
	if !strings.Contains(out, `\"odd\"`) || !strings.Contains(out, `\n`) {
		t.Errorf("the label value is not escaped:\n%s", out)
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n"); lines != 2 {
		t.Errorf("the family rendered %d lines past the first:\n%s", lines, out)
	}
}

// An unbounded label turns one family into a series per distinct value,
// and a metrics endpoint that grows without bound is an outage rather
// than an observation.
func TestSeriesPastTheBoundAreCountedUnderOverflow(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("halite_ext_invocations_total", "Invocations.", "name")
	for i := 0; i < MaxSeries+50; i++ {
		c.With("ext" + itoa(i)).Inc()
	}
	out := render(t, r)
	if !strings.Contains(out, `halite_ext_invocations_total{name="__overflow__"} 51`) {
		t.Errorf("the overflow series is wrong:\n%s", firstLines(out, 4))
	}
	if got := strings.Count(out, "halite_ext_invocations_total{"); got != MaxSeries {
		t.Errorf("the family holds %d series, want %d", got, MaxSeries)
	}
}

// The exposition is diffed between two captures during an incident, so
// an unchanged registry must render identically.
func TestTheOrderIsStable(t *testing.T) {
	r := NewRegistry()
	r.Counter("halite_zzz_total", "Z.", "k").With("b").Inc()
	r.Counter("halite_zzz_total", "Z.", "k").With("a").Inc()
	r.Counter("halite_aaa_total", "A.").Inc()
	first := render(t, r)
	if second := render(t, r); first != second {
		t.Errorf("two renders differ:\n%s\n---\n%s", first, second)
	}
	if strings.Index(first, "halite_aaa_total") > strings.Index(first, "halite_zzz_total") {
		t.Errorf("families are not in name order:\n%s", first)
	}
	if strings.Index(first, `k="a"`) > strings.Index(first, `k="b"`) {
		t.Errorf("series are not in label order:\n%s", first)
	}
}

// Instrumentation is written unconditionally, so a program that exposes
// nothing must still be able to run every line of it.
func TestANilRegistryIsUsable(t *testing.T) {
	var r *Registry
	c := r.Counter("halite_x_total", "X.", "k")
	g := r.Gauge("halite_y", "Y.")
	h := r.Histogram("halite_z_seconds", "Z.", nil, "k")
	r.GaugeFunc("halite_w", "W.", func() float64 { return 1 })
	r.BuildInfo("hub")

	c.With("a").Inc()
	c.Inc()
	g.Set(2)
	g.Add(1)
	h.With("a").Observe(0.5)
	h.Observe(0.5)

	if out := render(t, r); out != "" {
		t.Errorf("a nil registry rendered %q", out)
	}
}

func TestAGaugeFuncIsReadAtExpositionTime(t *testing.T) {
	r := NewRegistry()
	depth := 0
	r.GaugeFunc("halite_reactor_queue_depth", "Queue depth.", func() float64 { return float64(depth) })
	if out := render(t, r); !strings.Contains(out, "halite_reactor_queue_depth 0") {
		t.Fatalf("wrong at first:\n%s", out)
	}
	depth = 7
	if out := render(t, r); !strings.Contains(out, "halite_reactor_queue_depth 7") {
		t.Errorf("the gauge did not follow its source:\n%s", out)
	}
}

func TestBuildInfoCarriesTheIdentityInItsLabels(t *testing.T) {
	r := NewRegistry()
	r.BuildInfo("hub")
	out := render(t, r)
	if !strings.Contains(out, `component="hub"`) || !strings.Contains(out, "go_version=") {
		t.Errorf("build info is incomplete:\n%s", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), " 1") {
		t.Errorf("build info is not 1:\n%s", out)
	}
}

// Declaring one name twice with a different shape would leave an
// exposition quietly missing half its series, found during the incident
// it was meant to explain.
func TestARedeclarationWithADifferentShapePanics(t *testing.T) {
	r := NewRegistry()
	r.Counter("halite_jobs_total", "Jobs.", "fun")
	defer func() {
		if recover() == nil {
			t.Error("redeclaring with different labels was accepted")
		}
	}()
	r.Counter("halite_jobs_total", "Jobs.", "node")
}

func TestTheWrongNumberOfLabelValuesPanics(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("halite_jobs_total", "Jobs.", "fun", "result")
	defer func() {
		if recover() == nil {
			t.Error("a short label set was accepted")
		}
	}()
	c.With("test.ping").Inc()
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// Every counter in SPEC 26.2 is written from the goroutine that did the
// work — a handler, a reactor worker, a beacon tick — so the registry is
// concurrent by construction.
func TestConcurrentObservationsAreNotLost(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("halite_job_returns_total", "Returns.", "result")
	h := r.Histogram("halite_job_duration_seconds", "Duration.", []float64{1}, "fun")

	const workers, each = 8, 500
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				c.With("ok").Inc()
				h.With("test.ping").Observe(0.5)
			}
		}()
	}
	wg.Wait()

	out := render(t, r)
	if !strings.Contains(out, `halite_job_returns_total{result="ok"} `+itoa(workers*each)) {
		t.Errorf("observations were lost:\n%s", out)
	}
	if !strings.Contains(out, `halite_job_duration_seconds_count{fun="test.ping"} `+itoa(workers*each)) {
		t.Errorf("histogram observations were lost:\n%s", out)
	}
}

// The text format allows one HELP and one TYPE per metric name in a
// document. Two components that both expose `halite_build_info` — which
// is what its `component` label is for — produce two of each when their
// expositions are concatenated, and a scraper rejects the whole body.
func TestMergingTwoExpositionsDeclaresEachFamilyOnce(t *testing.T) {
	api := "# HELP halite_build_info Build identity.\n" +
		"# TYPE halite_build_info gauge\n" +
		`halite_build_info{component="api"} 1` + "\n" +
		"# HELP halite_api_requests_total Requests.\n" +
		"# TYPE halite_api_requests_total counter\n" +
		`halite_api_requests_total{route="/v1/run"} 3` + "\n"
	hub := "# HELP halite_build_info Build identity.\n" +
		"# TYPE halite_build_info gauge\n" +
		`halite_build_info{component="hub"} 1` + "\n" +
		"# HELP halite_hub_nodes_connected Nodes.\n" +
		"# TYPE halite_hub_nodes_connected gauge\n" +
		"halite_hub_nodes_connected 4\n"

	out := Merge(api, hub)
	if got := strings.Count(out, "# HELP halite_build_info"); got != 1 {
		t.Errorf("halite_build_info is declared %d times:\n%s", got, out)
	}
	if got := strings.Count(out, "# TYPE halite_build_info"); got != 1 {
		t.Errorf("halite_build_info has %d type lines:\n%s", got, out)
	}
	for _, want := range []string{
		`halite_build_info{component="api"} 1`,
		`halite_build_info{component="hub"} 1`,
		"halite_hub_nodes_connected 4",
		`halite_api_requests_total{route="/v1/run"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the merge lost %q:\n%s", want, out)
		}
	}
	// Every series line still sits under its own declaration.
	if strings.Index(out, "# TYPE halite_api_requests_total") >
		strings.Index(out, `halite_api_requests_total{route="/v1/run"} 3`) {
		t.Errorf("a series is above its declaration:\n%s", out)
	}
}

// A comment explaining that a component's metrics are absent is the most
// important line in the body when it is there.
func TestMergingKeepsAnExplanatoryComment(t *testing.T) {
	out := Merge("# HELP halite_x A.\n# TYPE halite_x gauge\nhalite_x 1\n",
		"# the hub's metrics are absent: connection refused\n")
	if !strings.Contains(out, "# the hub's metrics are absent") {
		t.Errorf("the comment was dropped:\n%s", out)
	}
	if !strings.Contains(out, "halite_x 1") {
		t.Errorf("the metric was dropped:\n%s", out)
	}
}

func TestMergingIsStable(t *testing.T) {
	a := "# HELP halite_z Z.\n# TYPE halite_z counter\nhalite_z 1\n"
	b := "# HELP halite_a A.\n# TYPE halite_a counter\nhalite_a 2\n"
	first := Merge(a, b)
	if second := Merge(a, b); first != second {
		t.Errorf("two merges differ:\n%s\n---\n%s", first, second)
	}
	if strings.Index(first, "halite_a") > strings.Index(first, "halite_z") {
		t.Errorf("families are not in name order:\n%s", first)
	}
}
