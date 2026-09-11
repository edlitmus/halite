//go:build perf

package perf

import (
	"testing"
	"time"
)

// TestTheMeasuredTargetsAreMet runs each benchmarked row and fails if the
// measurement is over the number SPEC section 30 states.
//
// Behind a build tag, and run by `make perf` rather than by `make check`,
// for the reason the package comment gives: a wall clock on a shared
// runner measures the runner. What the tag buys is that the comparison
// exists somewhere and can be run deliberately -- on a release candidate,
// or after a change to the parser, the template engine or the compiler --
// instead of the targets being numbers nobody has ever checked, which is
// what they were until this package was written.
//
// The failure names the machine's own measurement, because "over the
// target" on an unknown machine is not yet a finding.
func TestTheMeasuredTargetsAreMet(t *testing.T) {
	run := map[string]func(*testing.B){
		"BenchmarkHighstateCompile": BenchmarkHighstateCompile,
		"BenchmarkPillarCompile":    BenchmarkPillarCompile,
	}

	for _, tg := range targets {
		if tg.Measured == "" {
			t.Logf("%-56s %s, waiting on %s", tg.Metric, tg.Method, tg.Waiting)
			continue
		}
		fn, ok := run[tg.Measured]
		if !ok {
			t.Fatalf("%q names %s and nothing here runs it", tg.Metric, tg.Measured)
		}

		res := testing.Benchmark(fn)
		if res.N == 0 {
			t.Fatalf("%s did not run", tg.Measured)
		}
		per := time.Duration(res.NsPerOp())
		t.Logf("%-56s %v (target %v, %d runs)", tg.Metric, per.Round(time.Millisecond), tg.Limit, res.N)
		if per > tg.Limit {
			t.Errorf("%s: %v per compile, over SPEC section 30's %v.\n"+
				"  measured on this machine, %d runs, %d B/op\n"+
				"  Either the target is missed or the machine is not one to measure on; "+
				"the number above says which is worth arguing about.",
				tg.Metric, per, tg.Limit, res.N, res.AllocedBytesPerOp())
		}
		if tg.Waiting != "" {
			t.Logf("%-56s partial: %s", "", tg.Waiting)
		}
	}
}
