package metrics

import (
	"io"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/version"
)

// ContentType is what a scraper is told the body is.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Write renders the whole registry in Prometheus text exposition
// format.
//
// Families are written in name order and series within a family in
// label order, so two scrapes of an unchanged registry are identical.
// That is not required by the format; it is required by anyone
// diffing two captures during an incident.
func (r *Registry) Write(w io.Writer) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	names := append([]string(nil), r.order...)
	families := make([]*family, 0, len(names))
	for _, n := range names {
		families = append(families, r.families[n])
	}
	r.mu.Unlock()
	sort.Slice(families, func(i, j int) bool { return families[i].name < families[j].name })

	var b strings.Builder
	for _, f := range families {
		f.writeTo(&b)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (f *family) writeTo(b *strings.Builder) {
	f.mu.Lock()
	read := f.read
	samples := make([]*sample, 0, len(f.order))
	for _, key := range f.order {
		samples = append(samples, f.series[key])
	}
	f.mu.Unlock()

	// The declaration is written even for a family nothing has observed
	// yet. SPEC 26.2's rule is that every bounded queue and every drop
	// path has a counter, and a rule like that is auditable only if a
	// scraper can see the counter exists before anything has dropped.
	b.WriteString("# HELP " + f.name + " " + escapeHelp(f.help) + "\n")
	b.WriteString("# TYPE " + f.name + " " + f.kind.String() + "\n")

	if read != nil {
		b.WriteString(f.name + " " + formatFloat(read()) + "\n")
		return
	}
	if len(samples) == 0 {
		// An unlabelled family reads as zero; a labelled one has no
		// series to name yet, and inventing a label value would be
		// inventing an observation.
		if len(f.labels) == 0 {
			b.WriteString(f.name + " 0\n")
		}
		return
	}

	sort.Slice(samples, func(i, j int) bool {
		return strings.Join(samples[i].values, "\x00") < strings.Join(samples[j].values, "\x00")
	})
	for _, s := range samples {
		if f.kind == histogramKind {
			f.writeHistogram(b, s)
			continue
		}
		b.WriteString(f.name + f.labelsOf(s.values, "", "") + " " +
			formatFloat(math.Float64frombits(s.single.Load())) + "\n")
	}
}

func (f *family) writeHistogram(b *strings.Builder, s *sample) {
	// Prometheus buckets are cumulative: a bucket holds everything at
	// or below its bound, so each one includes the ones before it.
	for i, upper := range f.buckets {
		b.WriteString(f.name + "_bucket" +
			f.labelsOf(s.values, "le", formatFloat(upper)) + " " +
			strconv.FormatUint(s.counts[i].Load(), 10) + "\n")
	}
	b.WriteString(f.name + "_bucket" + f.labelsOf(s.values, "le", "+Inf") + " " +
		strconv.FormatUint(s.total.Load(), 10) + "\n")
	b.WriteString(f.name + "_sum" + f.labelsOf(s.values, "", "") + " " +
		formatFloat(math.Float64frombits(s.sum.Load())) + "\n")
	b.WriteString(f.name + "_count" + f.labelsOf(s.values, "", "") + " " +
		strconv.FormatUint(s.total.Load(), 10) + "\n")
}

// labelsOf renders the label set, with one extra pair when a histogram
// bucket needs `le`.
func (f *family) labelsOf(values []string, extraName, extraValue string) string {
	if len(values) == 0 && extraName == "" {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, name := range f.labels {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name + `="` + escapeLabel(values[i]) + `"`)
	}
	if extraName != "" {
		if len(f.labels) > 0 {
			b.WriteByte(',')
		}
		b.WriteString(extraName + `="` + escapeLabel(extraValue) + `"`)
	}
	b.WriteByte('}')
	return b.String()
}

// escapeLabel escapes what the format reserves inside a label value.
//
// A label value carries a function name, an event tag, or a path, all
// of which come from outside the program. Without this a value holding
// a quote produces a line no scraper can parse — and one holding a
// newline produces a line the sender chose, which is worse.
func escapeLabel(v string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return replacer.Replace(v)
}

func escapeHelp(v string) string {
	replacer := strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	return replacer.Replace(v)
}

// formatFloat writes a number the way the exposition format wants it:
// an integer without a decimal point, and the three special values by
// name.
func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// BuildInfo registers `halite_build_info`, the SPEC 26.2 family whose
// value is always 1 and whose labels are the point.
func (r *Registry) BuildInfo(component string) {
	if r == nil {
		return
	}
	g := r.Gauge("halite_build_info",
		"Build identity; the value is always 1 and the labels carry the information.",
		"component", "version", "commit", "go_version", "fips")
	g.With(component, version.Version, version.Commit, runtime.Version(), strconv.FormatBool(version.FIPS())).Set(1)
}
