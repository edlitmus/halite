// Package perf is SPEC section 30: the performance targets, what measures
// each one, and the two that can be measured with a benchmark alone.
//
// SPEC 30 opens by saying the targets are given "with the measurement
// method, so they can be tested rather than asserted". Until this package
// existed, `grep "func Benchmark"` over the tree returned nothing, so all
// thirteen rows were asserted and none was tested.
//
// Two of the thirteen name a benchmark as their own method: the highstate
// compile and the pillar compile. Those two need no simulated fleet, no
// second machine and no clock discipline beyond `go test -bench`, so they
// are built here. The other eleven need the scale harness, a soak, or an
// integration matrix, and the table below carries each one with what it
// waits on, held to SPEC's own table by a guard so that a row cannot be
// forgotten or quietly reworded.
//
// # What a number from here means
//
// It means one machine compiled one generated tree. It is not a claim
// about a fleet, and the compile is not the whole of `state.apply` -- it
// is the parse, the render, the requisite resolution and the ordering,
// which is what SPEC's row names and what a node does before it touches
// anything.
//
//	make perf
//
// runs the benchmarks and checks the two measured rows against their
// targets. It is deliberately not part of `make check`: a wall clock on a
// shared CI runner measures the runner, and a gate that fails for that
// reason gets ignored, which is worse than not having it.
package perf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/value"
)

// method is how a target is measured, in SPEC 30's own words.
type method string

const (
	// byBenchmark is the method this package implements.
	byBenchmark method = "benchmark"
	// byHarness needs the simulated node harness of SPEC 30, which does
	// not exist.
	byHarness method = "harness"
	// bySoak needs a node left running and watched.
	bySoak method = "soak"
	// byIntegration needs a hub and a node on real machines.
	byIntegration method = "integration test"
)

// target is one row of SPEC section 30.
type target struct {
	// Metric is the row's first column, verbatim, which is what the
	// guard matches on.
	Metric string
	Method method
	// Measured names the benchmark that measures it, or is empty.
	Measured string
	// Limit is the number SPEC states, for the rows a benchmark
	// measures. `make perf` fails when a measurement is over it.
	Limit time.Duration
	// Waiting says what the row needs before it can be measured. Every
	// row without a benchmark has one, and a row with both is a mistake
	// the guard catches.
	Waiting string
}

// targets is SPEC section 30's table, with what measures each row.
//
// The Metric strings are copied from SPEC.md and `TestTheTableIsSpecsOwn`
// holds them to it in both directions, so a row renamed there fails here
// rather than silently ceasing to be tracked.
var targets = []target{
	{Metric: "Highstate compile, 500 states, 50 SLS files, heavy Jinja",
		Method: byBenchmark, Measured: "BenchmarkHighstateCompile", Limit: 2 * time.Second},
	{Metric: "Pillar compile, 200 pillar SLS",
		Method: byBenchmark, Measured: "BenchmarkPillarCompile", Limit: 500 * time.Millisecond,
		// The row names two numbers and only one of them has something
		// behind it. There is no pillar cache in this build -- SPEC
		// 12.8's `pillar_cache_disk` is inert and
		// `halite_pillar_cache_hits_total` is one of the two metric
		// families of SPEC 26.2 that nothing registers -- so the cold
		// number is measured and the cached one has nothing to measure.
		// Benchmarking a second compile and calling it "cached" would
		// report the cold number twice under two names.
		Waiting: "the cached half: no pillar cache exists, so only the cold number is measured"},

	{Metric: "Nodes per hub", Method: byHarness,
		Waiting: "the simulated node harness"},
	{Metric: "Nodes per estate with relays", Method: byHarness,
		Waiting: "the simulated node harness, with two relay tiers"},
	{Metric: "`test.ping` to 10,000 nodes", Method: byHarness,
		Waiting: "the simulated node harness"},
	{Metric: "`state.apply` dispatch to 10,000 nodes", Method: byHarness,
		Waiting: "the simulated node harness"},
	{Metric: "Hub memory at 20,000 nodes", Method: byHarness,
		Waiting: "the simulated node harness"},
	{Metric: "Reactor throughput", Method: byHarness,
		Waiting: "the simulated node harness, driving the event bus"},
	{Metric: "File server throughput", Method: byHarness,
		Waiting: "a 10 Gb link and a client that can saturate it"},
	{Metric: "Node memory, idle with 10 beacons", Method: bySoak,
		Waiting: "a node left running and watched"},
	{Metric: "Node CPU, idle", Method: bySoak,
		Waiting: "a node left running and watched"},
	{Metric: "Cold start to first highstate", Method: byIntegration,
		Waiting: "the integration matrix of SPEC 31, which does not exist"},
}

// ---- the guards ----

// TestTheTableIsSpecsOwn holds the table above to SPEC section 30's table
// in both directions.
//
// A target that is not tracked here is a target nobody is measuring, and a
// row here that SPEC no longer names is a row that will be read as still
// applying.
func TestTheTableIsSpecsOwn(t *testing.T) {
	rows := specTargetRows(t)

	mine := map[string]bool{}
	for _, tg := range targets {
		if mine[tg.Metric] {
			t.Errorf("%q has two rows in this table", tg.Metric)
		}
		mine[tg.Metric] = true
		if !rows[tg.Metric] {
			t.Errorf("this table carries %q, which SPEC section 30 does not name.\n"+
				"Either it was renamed there, or this row is stale.", tg.Metric)
		}
	}
	for metric := range rows {
		if !mine[metric] {
			t.Errorf("SPEC section 30 names %q and nothing here tracks it.\n"+
				"Add a row saying what measures it, or what it waits on.", metric)
		}
	}
}

// TestEveryTargetIsMeasuredOrSaysWhatItWaitsOn is the rule that keeps the
// table honest: a row is either measured by a named benchmark that exists,
// or it says what it needs. Never both, and never neither.
func TestEveryTargetIsMeasuredOrSaysWhatItWaitsOn(t *testing.T) {
	benchmarks := benchmarkNames(t)

	for _, tg := range targets {
		switch {
		case tg.Measured == "" && tg.Waiting == "":
			t.Errorf("%q neither names a benchmark nor says what it waits on", tg.Metric)
		case tg.Measured == "" && tg.Method == byBenchmark:
			t.Errorf("%q is measured by benchmark according to SPEC and names none", tg.Metric)
		case tg.Measured != "" && tg.Method != byBenchmark:
			t.Errorf("%q names the benchmark %s, but its method is %q",
				tg.Metric, tg.Measured, tg.Method)
		}
		if (tg.Measured != "") != (tg.Limit != 0) {
			t.Errorf("%q names %q and a limit of %v; a measured row needs both",
				tg.Metric, tg.Measured, tg.Limit)
		}
		if tg.Measured != "" && !benchmarks[tg.Measured] {
			t.Errorf("%q names the benchmark %s, which this package does not define",
				tg.Metric, tg.Measured)
		}
	}
}

// specTargetRows reads the first column of SPEC section 30's table.
func specTargetRows(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "SPEC.md"))
	if err != nil {
		t.Fatalf("SPEC.md: %v", err)
	}
	body := string(raw)
	i := strings.Index(body, "## 30. Performance targets")
	if i < 0 {
		t.Fatal("SPEC.md has no section 30; this package is reading a spec it was not written for")
	}
	body = body[i:]
	if j := strings.Index(body, "\n## "); j > 0 {
		body = body[:j]
	}

	rows := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 {
			continue
		}
		metric := strings.TrimSpace(cells[0])
		if metric == "" || metric == "Metric" || strings.HasPrefix(metric, "---") {
			continue
		}
		rows[metric] = true
	}
	if len(rows) == 0 {
		t.Fatal("SPEC section 30's table read as empty; the reader is broken, not the spec")
	}
	return rows
}

// benchmarkNames lists the benchmarks this package defines, read from its
// own source. A row naming a benchmark that was renamed or deleted is a
// row that measures nothing.
func benchmarkNames(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading this package: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			const prefix = "func Benchmark"
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			name := line[len("func "):]
			if k := strings.IndexByte(name, '('); k > 0 {
				out[name[:k]] = true
			}
		}
	}
	return out
}

// TestTheTreesHaveTheShapeSpecNames is what stops a benchmark from
// reporting a number for a tree that is not the one SPEC describes.
//
// It is the same lesson as the quota leg of DIVERGENCE 5.48: a test that
// runs and reaches no assertion looks like progress in a log and is not.
// A benchmark cannot fail, so if the generator drifts -- a template that
// stops rendering, an include that stops resolving, a loop bound that
// changes -- the only thing that would say so is this.
func TestTheTreesHaveTheShapeSpecNames(t *testing.T) {
	compiled := compileHighstate(t)
	if got := len(compiled.Low); got != highstateDecls {
		t.Errorf("the state tree compiles to %d chunks; SPEC section 30 names 500", got)
	}
	if got := len(compiled.SLS); got != slsFiles {
		t.Errorf("the state tree compiled %d SLS files; SPEC section 30 names 50", got)
	}

	// The Jinja has to have run. A renderer that returned its input
	// unchanged would still parse as YAML and still produce chunks, and
	// the compile would then be measuring the wrong work.
	var rendered bool
	for _, ch := range compiled.Low {
		if ch.ID == "app00-conf-0" {
			rendered = true
			raw, _ := ch.Args.Get("contents")
			contents, ok := raw.(string)
			if !ok {
				t.Fatalf("app00-conf-0 has no rendered contents: %#v", raw)
			}
			if !strings.Contains(contents, "APP00-production") {
				t.Errorf("the macro did not run: contents is %q", contents)
			}
			if !strings.Contains(contents, "package nginx 1") {
				t.Errorf("the loop over pillar did not run: contents is %q", contents)
			}
		}
	}
	if !rendered {
		t.Fatalf("the tree has no app00-conf-0; the loop that emits the declarations did not run")
	}

	pil := compilePillar(t)
	if got := len(pil.SLS); got != pillarFiles {
		t.Errorf("the pillar tree compiled %d SLS files; SPEC section 30 names 200", got)
	}
	// Every one of the 200 files writes a key of its own and a counter
	// under the same `shared:counters` map, so a merge that replaced
	// instead of recursing would leave one counter rather than 200. That
	// is the work the pillar benchmark is measuring, so it is asserted
	// rather than assumed.
	shared, ok := pil.Pillar.Get("shared")
	if !ok {
		t.Fatal("the compiled pillar has no `shared` key; the merge did not run")
	}
	sharedMap, ok := shared.(*value.Map)
	if !ok {
		t.Fatalf("`shared` is %T, not a map", shared)
	}
	counters, _ := sharedMap.Get("counters")
	countersMap, ok := counters.(*value.Map)
	if !ok {
		t.Fatalf("`shared:counters` is %T, not a map", counters)
	}
	if got := countersMap.Len(); got != pillarFiles {
		t.Errorf("`shared:counters` merged to %d entries, want one per pillar file (%d)",
			got, pillarFiles)
	}
	if got := pil.Pillar.Len(); got != pillarFiles+2 { // the per-file keys, plus shared and owners
		t.Errorf("the compiled pillar has %d top-level keys, want %d", got, pillarFiles+2)
	}
}

// ---- what the benchmarks compile ----

func compileHighstate(t testing.TB) *state.Compiled {
	t.Helper()
	c := highstateCompiler()
	out := c.CompileHighstate()
	if err := out.Err(); err != nil {
		t.Fatalf("the generated state tree does not compile:\n%v", err)
	}
	return out
}

func highstateCompiler() *state.Compiler {
	return &state.Compiler{
		Loader:   highstateTree(),
		Registry: builtin.New().States.Signatures(),
		Config: state.Config{
			NodeID: "web01.prod",
			Grains: nodeGrains(),
			Pillar: nodePillar(),
		},
	}
}

func compilePillar(t testing.TB) *pillar.Compiled {
	t.Helper()
	c := pillarCompiler()
	out := c.Compile()
	if err := out.Err(); err != nil {
		t.Fatalf("the generated pillar tree does not compile:\n%v", err)
	}
	return out
}

func pillarCompiler() *pillar.Compiler {
	return &pillar.Compiler{
		Loader: pillarTree(),
		Config: pillar.Config{
			NodeID: "web01.prod",
			Grains: nodeGrains(),
		},
	}
}

// ---- the benchmarks ----

// BenchmarkHighstateCompile measures SPEC section 30's highstate row:
// 500 states over 50 SLS files with heavy Jinja, under 2 s on the node.
//
// The tree is built once, outside the timer, because building it is this
// package's own work and not the compiler's. Everything the compiler does
// per run is inside: the loader is re-read, every file is re-rendered, and
// nothing is cached between iterations, which is what a node does on a
// highstate with a cold file server.
func BenchmarkHighstateCompile(b *testing.B) {
	c := highstateCompiler()
	if out := c.CompileHighstate(); out.Err() != nil {
		b.Fatalf("the generated state tree does not compile:\n%v", out.Err())
	}

	b.ReportAllocs()
	for b.Loop() {
		out := c.CompileHighstate()
		if len(out.Low) != highstateDecls {
			b.Fatalf("compiled %d chunks, want %d", len(out.Low), highstateDecls)
		}
	}
	reportMillis(b)
}

// BenchmarkPillarCompile measures the cold half of SPEC section 30's
// pillar row: 200 pillar SLS, under 500 ms on the hub.
//
// The cached half of that row is not here and cannot be: nothing in this
// build caches a compiled pillar. See the `Waiting` note on its row.
func BenchmarkPillarCompile(b *testing.B) {
	c := pillarCompiler()
	if out := c.Compile(); out.Err() != nil {
		b.Fatalf("the generated pillar tree does not compile:\n%v", out.Err())
	}

	b.ReportAllocs()
	for b.Loop() {
		out := c.Compile()
		if len(out.SLS) != pillarFiles {
			b.Fatalf("compiled %d pillar files, want %d", len(out.SLS), pillarFiles)
		}
	}
	reportMillis(b)
}

// reportMillis adds the unit SPEC 30 states its targets in, so a run can
// be read against the specification without arithmetic.
func reportMillis(b *testing.B) {
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ms/compile")
}
