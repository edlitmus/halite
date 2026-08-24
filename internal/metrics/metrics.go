// Package metrics writes Prometheus text exposition directly.
//
// SPEC 26.2 says the format is documented and stable and that generating
// it needs no client library, which is the whole reason this package
// exists: a dependency for a hundred lines of text formatting would be a
// dependency in the supply chain of a control plane, and SPEC 4.2 allows
// none.
//
// A nil *Registry is usable. Every constructor answers with a nil metric
// and every method on a nil metric does nothing, so a subsystem can be
// instrumented unconditionally and a program that exposes nothing pays
// for nothing.
package metrics

import (
	"math"
	"strings"
	"sync"
	"sync/atomic"
)

// MaxSeries bounds the series one family may hold, the overflow series
// included.
//
// Every label in SPEC 26.2 is written by something outside the program:
// a function name, an event tag, a beacon, an extension. An estate with
// a thousand distinct functions would otherwise turn one family into a
// thousand series, and a metrics endpoint that grows without bound is an
// outage rather than an observation. Past the bound the observations are
// still counted, under `__overflow__`, so the total stays right and the
// loss is visible rather than silent.
const MaxSeries = 512

// OverflowLabel is the label value a series past MaxSeries is counted
// under.
const OverflowLabel = "__overflow__"

// DefaultBuckets are seconds, from a fast local call to a long state
// run. SPEC 26.2 names durations for jobs, state runs, compilation, and
// reactions, and they share a range.
var DefaultBuckets = []float64{0.005, 0.025, 0.1, 0.5, 1, 5, 15, 60, 300, 900}

type kind int

const (
	counterKind kind = iota
	gaugeKind
	histogramKind
)

func (k kind) String() string {
	switch k {
	case gaugeKind:
		return "gauge"
	case histogramKind:
		return "histogram"
	default:
		return "counter"
	}
}

// Registry holds every family a program exposes.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
	order    []string
}

// NewRegistry answers with an empty registry.
func NewRegistry() *Registry {
	return &Registry{families: map[string]*family{}}
}

type family struct {
	name    string
	help    string
	kind    kind
	labels  []string
	buckets []float64

	mu     sync.Mutex
	series map[string]*sample
	order  []string
	// read is a gauge whose value is fetched at exposition time rather
	// than stored: a queue's depth is the queue's business, and a copy
	// kept in step with it is a second thing to get wrong.
	read func() float64
}

type sample struct {
	values []string
	// A counter or a gauge keeps one number, as float64 bits. A
	// histogram keeps its buckets, its count, and its sum.
	single atomic.Uint64
	counts []atomic.Uint64
	total  atomic.Uint64
	sum    atomic.Uint64
}

// register returns the family under name, creating it on the first call.
//
// Declaring one name twice with a different shape is a programming
// error, and it panics rather than shadowing: the alternative is an
// exposition quietly missing half its series, discovered during the
// incident it was meant to explain.
func (r *Registry) register(name, help string, k kind, labels []string, buckets []float64) *family {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.families[name]; ok {
		if f.kind != k || strings.Join(f.labels, ",") != strings.Join(labels, ",") {
			panic("metrics: " + name + " is registered with a different shape")
		}
		return f
	}
	f := &family{
		name: name, help: help, kind: k,
		labels: labels, buckets: buckets,
		series: map[string]*sample{},
	}
	r.families[name] = f
	r.order = append(r.order, name)
	return f
}

// bind finds or creates the sample for one label set.
func (f *family) bind(values []string) *sample {
	if f == nil {
		return nil
	}
	if len(values) != len(f.labels) {
		// A caller that names the wrong number of values would
		// otherwise write into a series whose labels do not line up
		// with its values, which reads as data rather than as a bug.
		panic("metrics: " + f.name + " takes " + itoa(len(f.labels)) + " label values")
	}
	key := strings.Join(values, "\x00")
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.series[key]; ok {
		return s
	}
	// The bound counts the overflow series itself, so a family never
	// holds more than MaxSeries series however many distinct values it
	// is handed.
	if len(f.order) >= MaxSeries-1 {
		values = overflowValues(len(f.labels))
		key = strings.Join(values, "\x00")
		if s, ok := f.series[key]; ok {
			return s
		}
	}
	s := &sample{values: append([]string(nil), values...)}
	if f.kind == histogramKind {
		s.counts = make([]atomic.Uint64, len(f.buckets))
	}
	f.series[key] = s
	f.order = append(f.order, key)
	return s
}

func overflowValues(n int) []string {
	values := make([]string, n)
	for i := range values {
		values[i] = OverflowLabel
	}
	return values
}

// Counter is a monotonic total.
type Counter struct {
	f *family
	s *sample
}

// Gauge is a value that goes up and down.
type Gauge struct {
	f *family
	s *sample
}

// Histogram is a distribution in fixed buckets.
type Histogram struct {
	f *family
	s *sample
}

// Counter declares a counter family. The arguments after the help text
// are label *names*; values are supplied per observation with With.
func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	f := r.register(name, help, counterKind, labels, nil)
	if f == nil {
		return nil
	}
	return &Counter{f: f}
}

// Gauge declares a gauge family.
func (r *Registry) Gauge(name, help string, labels ...string) *Gauge {
	f := r.register(name, help, gaugeKind, labels, nil)
	if f == nil {
		return nil
	}
	return &Gauge{f: f}
}

// GaugeFunc declares an unlabelled gauge read at exposition time.
func (r *Registry) GaugeFunc(name, help string, read func() float64) {
	f := r.register(name, help, gaugeKind, nil, nil)
	if f == nil {
		return
	}
	f.mu.Lock()
	f.read = read
	f.mu.Unlock()
}

// Histogram declares a histogram family. The buckets are upper bounds in
// the unit the metric's name states; nil takes DefaultBuckets.
func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *Histogram {
	if buckets == nil {
		buckets = DefaultBuckets
	}
	f := r.register(name, help, histogramKind, labels, buckets)
	if f == nil {
		return nil
	}
	return &Histogram{f: f}
}

// With names the label values, in the order the family declared them.
func (c *Counter) With(values ...string) *Counter {
	if c == nil {
		return nil
	}
	return &Counter{f: c.f, s: c.f.bind(values)}
}

// With names the label values.
func (g *Gauge) With(values ...string) *Gauge {
	if g == nil {
		return nil
	}
	return &Gauge{f: g.f, s: g.f.bind(values)}
}

// With names the label values.
func (h *Histogram) With(values ...string) *Histogram {
	if h == nil {
		return nil
	}
	return &Histogram{f: h.f, s: h.f.bind(values)}
}

// Inc adds one.
func (c *Counter) Inc() { c.Add(1) }

// Add adds to the total. A negative amount is ignored: a counter that
// went backwards would be read by every consumer as a restart.
func (c *Counter) Add(n float64) {
	if c == nil || n < 0 {
		return
	}
	addFloat(&c.sample().single, n)
}

// Set replaces the value.
func (g *Gauge) Set(v float64) {
	if g == nil {
		return
	}
	g.sample().single.Store(math.Float64bits(v))
}

// Add moves the value by n, which may be negative.
func (g *Gauge) Add(n float64) {
	if g == nil {
		return
	}
	addFloat(&g.sample().single, n)
}

// Observe records one measurement.
func (h *Histogram) Observe(v float64) {
	if h == nil {
		return
	}
	s := h.sample()
	for i, upper := range h.f.buckets {
		if v <= upper {
			s.counts[i].Add(1)
		}
	}
	s.total.Add(1)
	addFloat(&s.sum, v)
}

func (c *Counter) sample() *sample   { return orBind(c.s, c.f) }
func (g *Gauge) sample() *sample     { return orBind(g.s, g.f) }
func (h *Histogram) sample() *sample { return orBind(h.s, h.f) }

// orBind lets an unlabelled family be used without calling With.
func orBind(s *sample, f *family) *sample {
	if s != nil {
		return s
	}
	return f.bind(nil)
}

func addFloat(v *atomic.Uint64, n float64) {
	for {
		old := v.Load()
		next := math.Float64bits(math.Float64frombits(old) + n)
		if v.CompareAndSwap(old, next) {
			return
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
