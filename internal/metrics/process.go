package metrics

import (
	"math"
	"runtime"
	"runtime/metrics"
)

// Process registers the process and Go runtime families for one
// component (issue 718, item 5; DIVERGENCE 5.675).
//
// Until these existed the only way to see a hub's memory, descriptors
// or threads was from outside it: the scale runs used a shell sampler
// (`ps -o rss=`, `/proc/PID/fd`) on the hub's host, and an operator had
// nothing. SPEC 30's memory row is a resident-set figure, and #715 was a
// hub running out of threads, so both are questions an estate asks in
// production, not only in a lab.
//
// The names are the ones every Prometheus client library uses, so an
// existing dashboard or alert for a Go service works unchanged -- with
// one difference. Each series carries a `component` label, because
// halite-api merges the hub's exposition into its own (Merge), and two
// unlabelled `process_resident_memory_bytes` lines would be one series
// written twice, which a scraper rejects whole. It is the same reason
// halite_build_info has the label.
//
// A process family is registered only where this build can read it, and
// that is decided by reading it once here. Registering it everywhere and
// writing 0 where the platform has no reader would put a node at zero
// bytes on a dashboard, which is an observation nobody made. Where a
// read that worked once fails later, the series reads NaN for that
// scrape, which Prometheus stores as a gap rather than a value.
//
// What each platform can read, and how, is in process_<goos>.go:
//
//	                               Linux  FreeBSD  macOS  Windows
//	process_resident_memory_bytes  yes    yes      no     no
//	process_open_fds               yes    yes      yes    no
//	process_max_fds                yes    yes      yes    no
//
// The Go families are read from the runtime and exist everywhere.
func (r *Registry) Process(component string) {
	if r == nil {
		return
	}
	gauge := func(name, help string, read func() (float64, bool)) {
		if _, ok := read(); !ok {
			return
		}
		r.GaugeFuncLabel(name, help, "component", component, func() float64 {
			v, ok := read()
			if !ok {
				return math.NaN()
			}
			return v
		})
	}
	gauge("process_resident_memory_bytes",
		"Resident set size of this process, in bytes, as the kernel counts it.",
		residentBytes)
	gauge("process_open_fds",
		"File descriptors this process has open.",
		openFDs)
	gauge("process_max_fds",
		"This process's soft limit on open file descriptors (RLIMIT_NOFILE), as the Go runtime set it at start; a child it starts gets the original back.",
		maxFDs)

	gauge("go_goroutines",
		"Goroutines that currently exist.",
		func() (float64, bool) { return float64(runtime.NumGoroutine()), true })
	// Created, not live: the runtime does not count live threads, and
	// it destroys a thread only when a goroutine exits locked to one,
	// which halite does not do. So for this program the two are the same
	// number, and this is the one the runtime can give without asking
	// the kernel. It is also what every Go client library exports under
	// this name.
	gauge("go_threads",
		"OS threads the Go runtime has created.",
		func() (float64, bool) {
			n, _ := runtime.ThreadCreateProfile(nil)
			return float64(n), true
		})

	// From runtime/metrics rather than runtime.ReadMemStats, which stops
	// the world: a scrape every few seconds of a hub serving twenty
	// thousand nodes should not pause it. The mapping is the one the
	// Prometheus Go client uses for the same names.
	gauge("go_memstats_sys_bytes",
		"Memory the Go runtime has obtained from the OS, in bytes; includes memory since returned (go_memstats_heap_released_bytes).",
		runtimeBytes("/memory/classes/total:bytes"))
	gauge("go_memstats_heap_inuse_bytes",
		"Bytes in heap spans that hold at least one object, live or not yet swept.",
		runtimeBytes("/memory/classes/heap/objects:bytes", "/memory/classes/heap/unused:bytes"))
	gauge("go_memstats_heap_released_bytes",
		"Heap memory the Go runtime has returned to the OS, in bytes; counted in go_memstats_sys_bytes and not resident.",
		runtimeBytes("/memory/classes/heap/released:bytes"))
}

// runtimeBytes reads and sums runtime/metrics samples. A name this Go
// does not know reads as not available, so a family whose source was
// renamed is left out rather than exported at zero.
func runtimeBytes(names ...string) func() (float64, bool) {
	return func() (float64, bool) {
		samples := make([]metrics.Sample, len(names))
		for i, n := range names {
			samples[i].Name = n
		}
		metrics.Read(samples)
		var total float64
		for _, s := range samples {
			if s.Value.Kind() != metrics.KindUint64 {
				return 0, false
			}
			total += float64(s.Value.Uint64())
		}
		return total, true
	}
}
