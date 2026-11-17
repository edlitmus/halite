package metrics

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// scrape writes a registry and returns each series line's value by its
// name and labels, as written.
func scrape(t *testing.T, r *Registry) map[string]float64 {
	t.Helper()
	var b strings.Builder
	if err := r.Write(&b); err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(b.String(), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		out[line[:i]] = v
	}
	return out
}

// residentOnThisPlatform and fdsOnThisPlatform are the table in
// Process's comment. A platform that gains a reader has to change both.
func residentOnThisPlatform() bool {
	return runtime.GOOS == "linux" ||
		(runtime.GOOS == "freebsd" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"))
}

func fdsOnThisPlatform() bool { return residentOnThisPlatform() || runtime.GOOS == "darwin" }

// **The resident set is the one `ps` prints** (DIVERGENCE 5.675). The
// scale runs read the hub's memory with `ps -o rss=`, and SPEC 30's row
// is that figure, so the metric that replaces the sampler is held to it.
// 64 MiB is touched first so that the figure is mostly something this
// test put there, and the two reads, a few milliseconds apart, can be
// compared within 10%. On FreeBSD this is also what checks the
// kinfo_proc offsets: a wrong one either fails the layout checks, and
// the family is missing, or reads another field, and disagrees with ps.
func TestResidentMemoryIsWhatPSReports(t *testing.T) {
	r := NewRegistry()
	r.Process("t")
	got, present := scrape(t, r)[`process_resident_memory_bytes{component="t"}`]
	if !residentOnThisPlatform() {
		if present {
			t.Fatalf("process_resident_memory_bytes is exported on %s/%s, which has no reader", runtime.GOOS, runtime.GOARCH)
		}
		t.Skipf("no resident-set reader on %s/%s, and the family is correctly absent", runtime.GOOS, runtime.GOARCH)
	}
	if !present {
		t.Fatalf("process_resident_memory_bytes is missing on %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ballast := make([]byte, 64<<20)
	for i := range ballast {
		ballast[i] = byte(i)
	}
	ours := scrape(t, r)[`process_resident_memory_bytes{component="t"}`]
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	runtime.KeepAlive(ballast)
	if err != nil {
		t.Skipf("ps -o rss= is not available here to compare with: %v", err)
	}
	kib, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("ps -o rss= printed %q", out)
	}
	theirs := kib * 1024
	if ours < 64<<20 || math.Abs(ours-theirs) > 0.1*theirs {
		t.Fatalf("process_resident_memory_bytes = %.0f, ps -o rss= says %.0f (before the ballast: %.0f)", ours, theirs, got)
	}
}

// **process_open_fds moves by exactly what this process opens.** The
// count is read, sixteen files are opened, and it is read again; the
// difference must be sixteen. Then the files are closed and it must
// come back. Unlike a comparison with lsof or procstat, this does not
// depend on what those tools count (the cwd, the executable) or on the
// pipes exec.Command opens to run them.
func TestOpenDescriptorsCountWhatIsOpened(t *testing.T) {
	r := NewRegistry()
	r.Process("t")
	key := `process_open_fds{component="t"}`
	before, present := scrape(t, r)[key]
	if !fdsOnThisPlatform() {
		if present {
			t.Fatalf("process_open_fds is exported on %s/%s, which has no reader", runtime.GOOS, runtime.GOARCH)
		}
		t.Skipf("no descriptor reader on %s/%s, and the family is correctly absent", runtime.GOOS, runtime.GOARCH)
	}
	if !present {
		t.Fatalf("process_open_fds is missing on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	var files []*os.File
	for i := 0; i < 16; i++ {
		f, err := os.Create(filepath.Join(t.TempDir(), "f"+strconv.Itoa(i)))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	during := scrape(t, r)[key]
	for _, f := range files {
		f.Close()
	}
	after := scrape(t, r)[key]
	if during-before != 16 || after != before {
		t.Fatalf("process_open_fds went %.0f, then %.0f with 16 more open, then %.0f with them closed", before, during, after)
	}
}

// **process_max_fds is this process's own soft limit**, read by
// something outside the reader: /proc/self/limits on Linux, procstat -l
// on FreeBSD.
//
// Not `ulimit -n` in a child, which is what this test first did and
// which is wrong: the Go runtime changes its own soft limit at start
// and gives a child the original back. On this project's Mac (macOS 26,
// arm64) the shell's limit was 1048576, the runtime clamped its own to
// kern.maxfilesperproc, 184320, and a child printed 1048576 -- a
// different number from the right one. macOS has no tool that reads
// another process's limit, so there the test checks only that the
// family is present.
func TestMaxDescriptorsIsTheSoftLimit(t *testing.T) {
	r := NewRegistry()
	r.Process("t")
	got, present := scrape(t, r)[`process_max_fds{component="t"}`]
	if !fdsOnThisPlatform() {
		if present {
			t.Fatalf("process_max_fds is exported on %s/%s, which has no reader", runtime.GOOS, runtime.GOARCH)
		}
		t.Skipf("no limit reader on %s/%s, and the family is correctly absent", runtime.GOOS, runtime.GOARCH)
	}
	if !present || !(got > 0) {
		t.Fatalf("process_max_fds = %v, present %v", got, present)
	}
	var want string
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/self/limits")
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "Max open files") {
				want = strings.Fields(strings.TrimPrefix(line, "Max open files"))[0]
			}
		}
	case "freebsd":
		out, err := exec.Command("procstat", "-l", strconv.Itoa(os.Getpid())).Output()
		if err != nil {
			t.Fatalf("procstat -l: %v", err)
		}
		// `PID COMM RLIMIT SOFT HARD`; the soft limit follows the
		// resource's name, wherever the command name puts it.
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			for i := 0; i+1 < len(f); i++ {
				if f[i] == "openfiles" {
					want = f[i+1]
				}
			}
		}
	default:
		t.Skipf("nothing on %s reads another process's descriptor limit to compare %.0f with", runtime.GOOS, got)
	}
	if want == "" {
		t.Fatalf("found no open-files limit to compare process_max_fds = %.0f with", got)
	}
	if strconv.FormatFloat(got, 'f', -1, 64) != want {
		t.Fatalf("process_max_fds = %.0f, but this process's limit is %s", got, want)
	}
}

// **The Go families are present everywhere and say something.** A
// goroutine count below the one this test is running on, or a heap with
// nothing in it, would be a reader that returns zero.
func TestTheGoFamiliesAreReadEverywhere(t *testing.T) {
	r := NewRegistry()
	r.Process("t")
	got := scrape(t, r)
	for _, name := range []string{"go_goroutines", "go_threads", "go_memstats_sys_bytes", "go_memstats_heap_inuse_bytes"} {
		if v := got[name+`{component="t"}`]; !(v >= 1) {
			t.Errorf("%s = %v", name, v)
		}
	}
	if v, ok := got[`go_memstats_heap_released_bytes{component="t"}`]; !ok || v < 0 {
		t.Errorf("go_memstats_heap_released_bytes = %v, %v", v, ok)
	}
	if got[`go_memstats_heap_inuse_bytes{component="t"}`] > got[`go_memstats_sys_bytes{component="t"}`] {
		t.Errorf("the heap in use is more than everything the runtime has obtained: %v", got)
	}
}

// **The API's merged exposition stays valid.** halite-api merges the
// hub's metrics into its own, and both register these families. Without
// the component label that would be the same unlabelled series twice,
// which a scraper rejects as a whole body.
func TestProcessFamiliesMergeWithoutDuplicateSeries(t *testing.T) {
	var hub, api strings.Builder
	for c, b := range map[string]*strings.Builder{"hub": &hub, "api": &api} {
		r := NewRegistry()
		r.Process(c)
		if err := r.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(Merge(api.String(), hub.String()), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series := line[:strings.LastIndexByte(line, ' ')]
		if seen[series] {
			t.Fatalf("the merged exposition has %s twice", series)
		}
		seen[series] = true
	}
	if !seen[`go_goroutines{component="hub"}`] || !seen[`go_goroutines{component="api"}`] {
		t.Fatalf("a component's series is missing from the merge: %v", seen)
	}
}

// **Every process family is documented, in docs/metrics.md and in SPEC
// 26.2.** TestEveryRegisteredMetricIsDocumented and specaudit's 26.2
// check read `halite_` names out of the source, and these keep the
// names the Prometheus client libraries use, so neither would notice
// one of them arriving undocumented. This reads the families out of
// what Process actually exposes on this platform.
func TestProcessFamiliesAreDocumented(t *testing.T) {
	r := NewRegistry()
	r.Process("t")
	var b strings.Builder
	if err := r.Write(&b); err != nil {
		t.Fatal(err)
	}
	read := func(path ...string) string {
		raw, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, path...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	docs := read("docs", "metrics.md")
	spec := read("SPEC.md")
	spec = spec[strings.Index(spec, "### 26.2 Metrics"):]
	spec = spec[:strings.Index(spec, "### 26.3")]
	n := 0
	for _, line := range strings.Split(b.String(), "\n") {
		name, ok := strings.CutPrefix(line, "# TYPE ")
		if !ok {
			continue
		}
		name = strings.Fields(name)[0]
		n++
		if !strings.Contains(docs, "`"+name+"`") {
			t.Errorf("docs/metrics.md never names %s", name)
		}
		if !strings.Contains(spec, "`"+name+"{component}`") {
			t.Errorf("SPEC 26.2 does not list %s{component}", name)
		}
	}
	if n < 5 {
		t.Fatalf("Process exposed %d families; this check has stopped checking", n)
	}
}
