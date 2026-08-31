package metrics

import (
	"strings"
	"testing"
)

// A histogram nobody has observed still has to read as a histogram.
//
// It used to write `halite_x_seconds 0` under `# TYPE halite_x_seconds
// histogram`. That is not a histogram in any exposition format: the
// series a histogram has are `_bucket`, `_sum` and `_count`, and a bare
// family name is what a counter writes. A scraper that asks for
// `halite_x_seconds_count` on a hub that has not compiled pillar yet
// finds nothing, and `histogram_quantile` over the buckets has no
// series to read — so the family declared expressly so it could be seen
// before its first observation was the one kind that could not be.
//
// `promtool check metrics` accepts the old line, so this is checked
// here rather than left to the linter.
func TestAnUnobservedHistogramExposesItsBuckets(t *testing.T) {
	r := NewRegistry()
	r.Histogram("halite_probe_seconds", "Nothing has observed this.", []float64{0.5, 1})

	var b strings.Builder
	if err := r.Write(&b); err != nil {
		t.Fatal(err)
	}
	got := b.String()

	for _, want := range []string{
		`halite_probe_seconds_bucket{le="0.5"} 0`,
		`halite_probe_seconds_bucket{le="1"} 0`,
		`halite_probe_seconds_bucket{le="+Inf"} 0`,
		"halite_probe_seconds_sum 0",
		"halite_probe_seconds_count 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the exposition is missing %q:\n%s", want, got)
		}
	}

	// The malformed line the fix removed: the family name with no
	// suffix, which is what a counter writes and a histogram must not.
	for _, line := range strings.Split(got, "\n") {
		if line == "halite_probe_seconds 0" {
			t.Errorf("a histogram wrote a bare sample line %q, which "+
				"Prometheus discards", line)
		}
	}
}

// A labelled histogram still names no series before its first
// observation: inventing a label value would be inventing an
// observation.
func TestAnUnobservedLabelledHistogramNamesNoSeries(t *testing.T) {
	r := NewRegistry()
	r.Histogram("halite_probe_seconds", "Nothing has observed this.", nil, "fun")

	var b strings.Builder
	if err := r.Write(&b); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(line, "halite_probe_seconds") &&
			!strings.HasPrefix(line, "# ") {
			t.Errorf("a labelled histogram invented the series %q", line)
		}
	}
}

// And an observation still lands where it should, so the zero case has
// not been fixed by breaking the ordinary one.
func TestAnObservedHistogramStillCounts(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("halite_probe_seconds", "Observed once.", []float64{0.5, 1})
	h.Observe(0.75)

	var b strings.Builder
	if err := r.Write(&b); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		`halite_probe_seconds_bucket{le="0.5"} 0`,
		`halite_probe_seconds_bucket{le="1"} 1`,
		`halite_probe_seconds_bucket{le="+Inf"} 1`,
		"halite_probe_seconds_count 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the exposition is missing %q:\n%s", want, got)
		}
	}
}
