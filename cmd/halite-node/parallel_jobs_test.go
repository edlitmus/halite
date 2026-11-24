package main

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// How a job may share the node, from the function, the node's setting,
// the operator's flag, and whether the job is signed (SPEC 9.6, 25.6).
func TestAJobsSchedulingFollowsTheFunctionTheSettingAndTheFlag(t *testing.T) {
	cases := []struct {
		name          string
		config        string
		fun           string
		parallel      bool
		signed        bool
		wantExclusive bool
		wantFamily    string
	}{
		{"a job runs alone by default", "", "pkg.install", false, false, true, "pkg"},
		{"--parallel lets an unsigned job share", "", "pkg.install", true, false, false, "pkg"},
		{"--parallel on a signed job is ignored", "", "pkg.install", true, true, true, "pkg"},
		{"parallel_jobs lets every job share", "parallel_jobs: true\n", "service.restart", false, false, false, "service"},
		{"parallel_jobs applies to a signed job too", "parallel_jobs: true\n", "service.restart", false, true, false, "service"},
		{"a state run is alone even with parallel_jobs", "parallel_jobs: true\n", "state.apply", false, false, true, "state"},
		{"a state run is alone even with --parallel", "", "state.highstate", true, false, true, "state"},
		{"an unreadable parallel_jobs runs the job alone", "parallel_jobs: maybe\n", "pkg.install", false, false, true, "pkg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := nodeForEvidence(t, tc.config)
			j := &job.Job{JID: "20261121T130000000001", Fun: tc.fun, Parallel: tc.parallel}
			if tc.signed {
				j.Signature = "c2lnbmVk"
			}

			exclusive, family := n.schedulingOf(j)

			if exclusive != tc.wantExclusive || family != tc.wantFamily {
				t.Errorf("exclusive=%v family=%q, want %v %q", exclusive, family, tc.wantExclusive, tc.wantFamily)
			}
		})
	}
}

// queueOf is an executor whose Run is never started, so that a test can
// call startable and release itself and see every decision.
func queueOf(t *testing.T, config string) *executor {
	t.Helper()
	n := nodeForEvidence(t, config)
	return newExecutor(n, 16, func(*job.Return) {})
}

func add(t *testing.T, e *executor, jid, fun string) *pending {
	t.Helper()
	p := &pending{job: &job.Job{JID: job.ID(jid), Fun: fun}, finish: e.returns}
	if err := e.enqueue(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func started(e *executor) []string {
	var out []string
	for _, p := range e.startable() {
		out = append(out, string(p.job.JID))
	}
	return out
}

func finish(e *executor, p *pending) {
	e.mu.Lock()
	e.release(p)
	e.mu.Unlock()
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Jobs of different families run together; jobs of one family take turns.
func TestParallelJobsShareTheNodeButNotAFamily(t *testing.T) {
	e := queueOf(t, "parallel_jobs: true\n")
	pkg1 := add(t, e, "1", "pkg.install")
	add(t, e, "2", "pkg.remove")
	add(t, e, "3", "service.restart")

	if got := started(e); !sameList(got, []string{"1", "3"}) {
		t.Fatalf("started %v, want the first pkg job and the service job", got)
	}
	if got := started(e); len(got) != 0 {
		t.Fatalf("started %v while the pkg lock was held", got)
	}
	finish(e, pkg1)
	if got := started(e); !sameList(got, []string{"2"}) {
		t.Fatalf("started %v once the pkg lock was free, want the second pkg job", got)
	}
}

// A state run waits for every running job, and nothing that arrives after
// it passes it -- or a steady stream of parallel jobs would keep the node
// from ever being empty, and the highstate would never run.
func TestAStateRunIsNotStarvedByParallelJobs(t *testing.T) {
	e := queueOf(t, "parallel_jobs: true\n")
	pkg := add(t, e, "1", "pkg.install")
	if got := started(e); !sameList(got, []string{"1"}) {
		t.Fatalf("started %v, want the pkg job", got)
	}
	state := add(t, e, "2", "state.apply")
	add(t, e, "3", "service.restart")

	if got := started(e); len(got) != 0 {
		t.Fatalf("started %v while a job was running and a state run was waiting", got)
	}
	finish(e, pkg)
	if got := started(e); !sameList(got, []string{"2"}) {
		t.Fatalf("started %v once the node was empty, want only the state run", got)
	}
	if got := started(e); len(got) != 0 {
		t.Fatalf("started %v beside a state run", got)
	}
	finish(e, state)
	if got := started(e); !sameList(got, []string{"3"}) {
		t.Fatalf("started %v after the state run, want the service job", got)
	}
}

// Without parallel_jobs or --parallel, one job at a time, as before.
func TestJobsRunOneAtATimeByDefault(t *testing.T) {
	e := queueOf(t, "")
	first := add(t, e, "1", "pkg.install")
	add(t, e, "2", "service.restart")

	if got := started(e); !sameList(got, []string{"1"}) {
		t.Fatalf("started %v, want only the first", got)
	}
	if got := started(e); len(got) != 0 {
		t.Fatalf("started %v beside a serial job", got)
	}
	finish(e, first)
	if got := started(e); !sameList(got, []string{"2"}) {
		t.Fatalf("started %v, want the second", got)
	}
}

// The whole path, from the stream to the return: with parallel_jobs, two
// one-second jobs of different families finish in about one second; with
// it off, the same two take about two.
//
// Once in CI (issue 808) the parallel case took 2.001s, and the failure
// said only that. It could not tell a job that waited for the other from
// two jobs that reached the queue a second apart, or from an executor
// that started late. So each run keeps a timeline -- when each accept
// returned, when each job's function was entered and left, when each
// return arrived -- and, if both jobs have not started by the time they
// should have, a snapshot of the queue and of every goroutine taken at
// that moment, while the waiting is still happening rather than after it.
// A failure prints all of it. A pass prints nothing.
func TestParallelJobsOverlapOnTheNodeAndSerialJobsDoNot(t *testing.T) {
	for _, tc := range []struct {
		config      string
		wantOverlap bool
	}{
		{"parallel_jobs: true\n", true},
		{"", false},
	} {
		n := nodeForEvidence(t, tc.config)
		n.refusals = make(chan *job.Return, 4)
		timeline := &jobTimeline{}
		bothStarted := make(chan struct{})
		var startedCount atomic.Int32
		for _, module := range []string{"napa", "napb"} {
			n.registry.Exec.Add(exec.Module{
				Sig: signature.Signature{Module: module, Function: "nap", Doc: "sleeps for a second"},
				Fn: func(*exec.Context, *value.Map) (any, error) {
					timeline.note(module + ".nap entered")
					if startedCount.Add(1) == 2 {
						close(bothStarted)
					}
					time.Sleep(time.Second)
					timeline.note(module + ".nap left")
					return true, nil
				},
			})
		}
		returns := make(chan *job.Return, 2)
		n.executor = newExecutor(n, 4, func(r *job.Return) { returns <- r })
		stop := make(chan struct{})

		go n.executor.Run(stop)
		timeline.begin()
		// On its own goroutine, because what it is looking for may be the
		// test goroutine itself, stuck in acceptJob. Only where the jobs
		// are meant to overlap: in the serial case the second job waiting
		// is the point.
		snapshot := make(chan string, 1)
		go func() {
			if !tc.wantOverlap {
				snapshot <- ""
				return
			}
			select {
			case <-bothStarted:
				snapshot <- ""
			case <-time.After(700 * time.Millisecond):
				snapshot <- stalledSnapshot(n.executor, timeline)
			}
		}()
		for i, fun := range []string{"napa.nap", "napb.nap"} {
			msg := jobMessage(t, "20261121T13000000010"+string(rune('0'+i)))
			msg.Fun = fun
			n.acceptJob(msg)
			timeline.note("accepted " + fun)
		}
		var got []*job.Return
		for range 2 {
			select {
			case r := <-returns:
				timeline.note("return for " + r.Fun)
				got = append(got, r)
				if !r.Success {
					t.Fatalf("%q: a job failed: %s", tc.config, r.Return)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%q: a job never returned\n%s", tc.config, timeline)
			}
		}
		elapsed := timeline.since()
		close(stop)

		overlapped := elapsed < 1800*time.Millisecond
		if overlapped != tc.wantOverlap {
			t.Errorf("config %q: two one-second jobs took %v; want overlap=%v\n%s%s%s",
				tc.config, elapsed, tc.wantOverlap, timeline, returnTimes(got), <-snapshot)
		}
	}
}

// jobTimeline is what happened in one run of the overlap test, with each
// event's offset from the start.
type jobTimeline struct {
	mu     sync.Mutex
	start  time.Time
	events []string
}

func (l *jobTimeline) begin() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.start = time.Now()
}

func (l *jobTimeline) since() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return time.Since(l.start)
}

func (l *jobTimeline) note(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fmt.Sprintf("  +%-8s %s", time.Since(l.start).Round(time.Millisecond), event))
}

func (l *jobTimeline) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return "timeline:\n" + strings.Join(l.events, "\n") + "\n"
}

// returnTimes is the node's own account of each job: the start it
// stamped and the duration it measured, which do not depend on the
// test's clock reads.
func returnTimes(returns []*job.Return) string {
	var b strings.Builder
	b.WriteString("returns:\n")
	for _, r := range returns {
		fmt.Fprintf(&b, "  %s start=%s duration_ms=%d\n", r.Fun, r.StartTime, r.DurationMS)
	}
	return b.String()
}

// stalledSnapshot is the queue and every goroutine at the moment both jobs
// should have been running and were not.
//
// TryLock rather than Lock: if the executor's mutex is what is held, that
// is the finding, and waiting for it would hide it.
func stalledSnapshot(e *executor, timeline *jobTimeline) string {
	var b strings.Builder
	fmt.Fprintf(&b, "at +700ms both jobs had not started.\n%s", timeline)
	if e.mu.TryLock() {
		var waiting []string
		for _, p := range e.waiting {
			waiting = append(waiting, fmt.Sprintf("%s(exclusive=%v family=%s)", p.job.Fun, p.exclusive, p.family))
		}
		fmt.Fprintf(&b, "executor: running=%d exclusive=%v families=%v waiting=%v\n",
			e.running, e.exclusive, e.families, waiting)
		e.mu.Unlock()
	} else {
		b.WriteString("executor: its mutex was held\n")
	}
	stacks := make([]byte, 1<<20)
	stacks = stacks[:runtime.Stack(stacks, true)]
	b.WriteString("goroutines:\n")
	b.Write(stacks)
	return b.String()
}
