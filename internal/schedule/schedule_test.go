package schedule

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

func parseSchedule(t *testing.T, src string) []*Job {
	t.Helper()
	jobs, err := parseScheduleErr(src)
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

func parseScheduleErr(src string) ([]*Job, error) {
	doc, _, err := yaml.Parse([]byte(src), yaml.Options{File: "node.yaml"})
	if err != nil {
		return nil, err
	}
	root, ok := doc.(*value.Map)
	if !ok {
		return nil, errString("the fixture is not a mapping")
	}
	raw, _ := root.Get("schedule")
	return Parse(raw, time.UTC)
}

type errString string

func (e errString) Error() string { return string(e) }

// SPEC 20.1's example, which is what an estate's configuration looks
// like.
func TestTheSpecExampleParses(t *testing.T) {
	jobs := parseSchedule(t, `
schedule:
  nightly_highstate:
    function: state.apply
    cron: '17 3 * * *'
    splay: 900
    maxrunning: 1
    return_job: True
    kwargs:
      queue: True
  collect_inventory:
    every: 15m
    function: grains.items
    return_job: False
    run_on_start: True
`)
	if len(jobs) != 2 {
		t.Fatalf("parsed %d jobs", len(jobs))
	}

	inventory, nightly := jobs[0], jobs[1]
	if nightly.Name != "nightly_highstate" || nightly.Cron == nil {
		t.Fatalf("the nightly job parsed as %+v", nightly)
	}
	if nightly.Splay != 15*time.Minute {
		t.Errorf("splay: 900 came to %s", nightly.Splay)
	}
	if !nightly.ReturnJob || nightly.MaxRunning != 1 {
		t.Errorf("the modifiers parsed as %+v", nightly)
	}
	if nightly.Kwargs == nil {
		t.Error("the kwargs did not survive")
	}

	if inventory.Interval != 15*time.Minute || inventory.ReturnJob || !inventory.RunOnStart {
		t.Errorf("the inventory job parsed as %+v", inventory)
	}
}

// A definition that cannot be answered has to be refused, not guessed
// at: a schedule is written once and trusted for years.
func TestABadScheduleIsRefused(t *testing.T) {
	cases := map[string]string{
		"no function":     "schedule:\n  a:\n    cron: '* * * * *'\n",
		"no timing":       "schedule:\n  a:\n    function: test.ping\n",
		"two timings":     "schedule:\n  a:\n    function: test.ping\n    cron: '* * * * *'\n    every: 5m\n",
		"unknown setting": "schedule:\n  a:\n    function: test.ping\n    every: 5m\n    nonsense: 1\n",
		"a bad cron":      "schedule:\n  a:\n    function: test.ping\n    cron: 'nonsense'\n",
		"a bad time":      "schedule:\n  a:\n    function: test.ping\n    when: 'half past something'\n",
		"a bad zone":      "schedule:\n  a:\n    function: test.ping\n    every: 5m\n    timezone: 'Mars/Olympus'\n",
		"not a mapping":   "schedule:\n  - a\n",
	}
	for name, src := range cases {
		if _, err := parseScheduleErr(src); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// `seconds` and `minutes` together are one interval, which reads well
// and which Salt allows.
func TestIntervalUnitsAddUp(t *testing.T) {
	jobs := parseSchedule(t, `
schedule:
  a:
    function: test.ping
    minutes: 5
    seconds: 30
`)
	if got := jobs[0].Interval; got != 5*time.Minute+30*time.Second {
		t.Errorf("the interval is %s", got)
	}
}

// `until` ends a job; `after` delays its first run; `range` and
// `skip_during_range` are windows a recurring job passes through rather
// than the end of it.
func TestTheBoundsAndWindows(t *testing.T) {
	base := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)

	jobs := parseSchedule(t, `
schedule:
  bounded:
    function: test.ping
    cron: '0 * * * *'
    after: '2026-08-23 04:00'
    until: '2026-08-23 08:00'
  windowed:
    function: test.ping
    cron: '0 * * * *'
    skip_during_range:
      start: '2026-08-23 01:00'
      end: '2026-08-23 05:00'
`)
	// Sorted by name, so `bounded` comes first.
	bounded, windowed := jobs[0], jobs[1]
	if bounded.Name != "bounded" || windowed.Name != "windowed" {
		t.Fatalf("the jobs are %s and %s", bounded.Name, windowed.Name)
	}

	next, ok := bounded.Next(base)
	if !ok || !next.Equal(time.Date(2026, 8, 23, 4, 0, 0, 0, time.UTC)) {
		t.Errorf("`after` gave %s (%v)", next, ok)
	}
	if _, ok := bounded.Next(time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)); ok {
		t.Error("`until` did not end the job")
	}

	// The window is passed through rather than ending the job: the hour
	// before it fires, the hours inside it do not, and the job resumes
	// after.
	next, ok = windowed.Next(time.Date(2026, 8, 22, 23, 30, 0, 0, time.UTC))
	if !ok || !next.Equal(time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("the fire before the window is %s (%v)", next, ok)
	}
	after, ok := windowed.Next(time.Date(2026, 8, 23, 1, 0, 0, 0, time.UTC))
	if !ok || !after.Equal(time.Date(2026, 8, 23, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("the skip window gave %s (%v)", after, ok)
	}
}

// `enabled: False` is a job that is written down and does not run.
func TestADisabledJobNeverFires(t *testing.T) {
	jobs := parseSchedule(t, `
schedule:
  a:
    function: test.ping
    every: 1m
    enabled: False
`)
	if _, ok := jobs[0].Next(time.Now()); ok {
		t.Error("a disabled job claims a next fire time")
	}
}

// `maxrunning` is what stops a job that takes longer than its interval
// from piling up until the node falls over.
func TestMaxRunningHoldsAJobBack(t *testing.T) {
	job := &Job{
		Name: "slow", Function: "test.sleep", Interval: 10 * time.Millisecond,
		Enabled: true, MaxRunning: 1, Location: time.UTC,
	}
	release := make(chan struct{})
	var started int
	var mu sync.Mutex
	e := &Engine{
		Jobs: []*Job{job},
		Wake: 5 * time.Millisecond,
		Execute: func(ctx context.Context, r Run) error {
			mu.Lock()
			started++
			mu.Unlock()
			<-release
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// Several intervals pass while the first run is still going.
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	n := started
	mu.Unlock()
	if n != 1 {
		t.Errorf("%d runs started with maxrunning 1", n)
	}
	close(release)
}

// `run_on_start` and `@reboot` both mean now, which is what makes a
// node apply its highstate when it comes up.
func TestRunOnStartFiresImmediately(t *testing.T) {
	for _, src := range []string{
		"schedule:\n  a:\n    function: state.apply\n    every: 1h\n    run_on_start: True\n",
		"schedule:\n  a:\n    function: state.apply\n    cron: '@reboot'\n",
	} {
		jobs := parseSchedule(t, src)
		fired := make(chan Run, 1)
		e := &Engine{
			Jobs: jobs,
			Wake: 5 * time.Millisecond,
			Execute: func(ctx context.Context, r Run) error {
				select {
				case fired <- r:
				default:
				}
				return nil
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		go e.Run(ctx)
		select {
		case <-fired:
		case <-time.After(2 * time.Second):
			t.Errorf("%q did not run at start", src)
		}
		cancel()
	}
}

// `catchup` runs a job once whose window closed while the node was off.
// Without it a missed run does not backfill, which is the default.
func TestCatchupRunsAMissedJobOnce(t *testing.T) {
	jobs := parseSchedule(t, `
schedule:
  a:
    function: state.apply
    cron: '0 3 * * *'
    catchup: True
`)
	job := jobs[0]
	// It last ran two days ago, so 03:00 yesterday was missed.
	job.LastRun = time.Now().Add(-48 * time.Hour)

	fired := make(chan Run, 4)
	e := &Engine{
		Jobs: jobs,
		Wake: 5 * time.Millisecond,
		Execute: func(ctx context.Context, r Run) error {
			select {
			case fired <- r:
			default:
			}
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	select {
	case r := <-fired:
		if !r.Fire.Before(time.Now()) {
			t.Errorf("the catch-up run claims to be due at %s", r.Fire)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the missed run was not caught up")
	}

	// And only once: the next is on the clock.
	select {
	case r := <-fired:
		t.Errorf("a second catch-up ran, due at %s", r.Fire)
	case <-time.After(200 * time.Millisecond):
	}
}

// Splay spreads a fleet's identical schedules so that a thousand nodes
// do not arrive at the hub in the same second.
func TestSplayIsWithinItsRange(t *testing.T) {
	job := &Job{Splay: 900 * time.Second}
	if got := job.SplayFor(func(int64) int64 { return 0 }); got != 0 {
		t.Errorf("the low end is %s", got)
	}
	if got := job.SplayFor(func(n int64) int64 { return n - 1 }); got >= 900*time.Second {
		t.Errorf("the high end is %s", got)
	}

	ranged := &Job{SplayStart: time.Minute, Splay: 2 * time.Minute}
	if got := ranged.SplayFor(func(int64) int64 { return 0 }); got != time.Minute {
		t.Errorf("a splay range starts at %s", got)
	}

	none := &Job{}
	if got := none.SplayFor(func(int64) int64 { return 100 }); got != 0 {
		t.Errorf("no splay gave %s", got)
	}
}

// `once_fmt` is Python's strftime, because an estate that has one has
// written it that way.
func TestOnceFmtTranslates(t *testing.T) {
	jobs := parseSchedule(t, `
schedule:
  a:
    function: test.ping
    once: '2027/01/02 03:04'
    once_fmt: '%Y/%m/%d %H:%M'
`)
	want := time.Date(2027, 1, 2, 3, 4, 0, 0, time.UTC)
	if !jobs[0].Once.Equal(want) {
		t.Errorf("once parsed as %s, want %s", jobs[0].Once, want)
	}

	if _, err := parseScheduleErr(
		"schedule:\n  a:\n    function: test.ping\n    once: 'x'\n    once_fmt: '%Q'\n"); err == nil {
		t.Error("an unknown strftime directive was accepted")
	} else if !strings.Contains(err.Error(), "%Q") {
		t.Errorf("the refusal says %q", err)
	}
}
