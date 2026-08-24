package schedule

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

func controlEngine() (*Engine, *[]Run, *sync.Mutex) {
	var mu sync.Mutex
	var ran []Run
	e := &Engine{
		Wake:     5 * time.Millisecond,
		Location: time.UTC,
		Execute: func(ctx context.Context, r Run) error {
			mu.Lock()
			ran = append(ran, r)
			mu.Unlock()
			return nil
		},
	}
	return e, &ran, &mu
}

func ranCount(mu *sync.Mutex, ran *[]Run) int {
	mu.Lock()
	defer mu.Unlock()
	return len(*ran)
}

// A schedule that can only be changed by restarting the node is one
// nobody changes when they need to.
func TestAJobCanBeAddedToARunningSchedule(t *testing.T) {
	e, ran, mu := controlEngine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	time.Sleep(40 * time.Millisecond)
	if n := ranCount(mu, ran); n != 0 {
		t.Fatalf("%d jobs ran with an empty schedule", n)
	}

	err := e.Add("ping", value.MapOf(
		"function", "test.ping",
		"seconds", int64(1),
		"run_on_start", true,
	))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for ranCount(mu, ran) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the added job never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}

	listed := e.List()
	if !listed.Has("ping") {
		t.Errorf("the listing is %v", listed.StringKeys())
	}
}

// A definition that cannot be answered is refused at the point of
// asking.
func TestAddingABadJobIsRefused(t *testing.T) {
	e, _, _ := controlEngine()
	cases := map[string]*value.Map{
		"no function": value.MapOf("every", "5m"),
		"no timing":   value.MapOf("function", "test.ping"),
		"a bad cron":  value.MapOf("function", "test.ping", "cron", "nonsense"),
	}
	for name, def := range cases {
		if err := e.Add("j", def); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := e.Add("", value.MapOf("function", "test.ping", "every", "5m")); err == nil {
		t.Error("a job with no name was accepted")
	}
}

func TestAddingTheSameJobTwiceIsRefused(t *testing.T) {
	e, _, _ := controlEngine()
	def := value.MapOf("function", "test.ping", "every", "1h")
	if err := e.Add("a", def); err != nil {
		t.Fatal(err)
	}
	err := e.Add("a", def)
	if err == nil {
		t.Fatal("the same job was added twice")
	}
	if !strings.Contains(err.Error(), "modify") {
		t.Errorf("the refusal should name what to use instead: %q", err)
	}
}

// Holding the schedule stops every job without forgetting any, and
// letting it go restores exactly what was there.
func TestDisablingHoldsTheWholeSchedule(t *testing.T) {
	e, ran, mu := controlEngine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if err := e.Add("ping", value.MapOf(
		"function", "test.ping", "seconds", int64(1), "run_on_start", true)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for ranCount(mu, ran) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the job never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := e.SetEnabled("", false); err != nil {
		t.Fatal(err)
	}
	settled := ranCount(mu, ran)
	time.Sleep(80 * time.Millisecond)
	if ranCount(mu, ran) != settled {
		t.Error("a held schedule kept running jobs")
	}
	// A held schedule says so rather than reporting a time it will not
	// fire at.
	entry, _ := e.List().Get("ping")
	m, _ := entry.(*value.Map)
	if enabled, _ := m.Get("enabled"); enabled != false {
		t.Errorf("a held job reports enabled=%v", enabled)
	}
	if next, _ := m.Get("next_fire_time"); next != nil {
		t.Errorf("a held job reports a next fire time of %v", next)
	}

	if err := e.SetEnabled("", true); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for ranCount(mu, ran) == settled {
		if time.Now().After(deadline) {
			t.Fatal("the schedule did not resume")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// `run_job` runs one job now without disturbing when it next runs on
// its own, which is the whole reason to reach for it.
func TestRunJobRunsNowAndLeavesTheScheduleAlone(t *testing.T) {
	e, ran, mu := controlEngine()
	if err := e.Add("nightly", value.MapOf(
		"function", "state.apply", "cron", "17 3 * * *")); err != nil {
		t.Fatal(err)
	}
	before, ok, err := e.NextFireTime("nightly")
	if err != nil || !ok {
		t.Fatalf("no next fire time: %v %v", ok, err)
	}

	if err := e.RunJob(context.Background(), "nightly"); err != nil {
		t.Fatal(err)
	}
	if n := ranCount(mu, ran); n != 1 {
		t.Fatalf("run_job ran the job %d times", n)
	}

	after, ok, err := e.NextFireTime("nightly")
	if err != nil || !ok {
		t.Fatalf("no next fire time after run_job: %v %v", ok, err)
	}
	if !after.Equal(before) {
		t.Errorf("run_job moved the schedule from %s to %s", before, after)
	}

	if err := e.RunJob(context.Background(), "nosuchjob"); err == nil {
		t.Error("running a job that is not scheduled reported success")
	}
}

// Modifying keeps a job's last run, so an interval job does not start
// its clock again every time someone adjusts it.
func TestModifyingKeepsTheLastRun(t *testing.T) {
	e, _, _ := controlEngine()
	if err := e.Add("a", value.MapOf("function", "test.ping", "every", "1h")); err != nil {
		t.Fatal(err)
	}
	last := time.Now().Add(-30 * time.Minute)
	e.mu.Lock()
	e.Jobs[0].LastRun = last
	e.mu.Unlock()

	if err := e.Modify("a", value.MapOf("function", "test.ping", "every", "2h")); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	got := e.Jobs[0].LastRun
	interval := e.Jobs[0].Interval
	e.mu.Unlock()
	if !got.Equal(last) {
		t.Errorf("modifying reset the last run to %s", got)
	}
	if interval != 2*time.Hour {
		t.Errorf("the new interval is %s", interval)
	}

	if err := e.Modify("nosuchjob", value.MapOf("function", "test.ping", "every", "1h")); err == nil {
		t.Error("modifying a job that is not scheduled reported success")
	}
}

// Reload replaces the running set and keeps what survived, which is
// what makes `schedule.reload` a way to discard runtime changes rather
// than a way to restart every interval in the estate.
func TestReloadKeepsTheLastRunOfSurvivingJobs(t *testing.T) {
	e, _, _ := controlEngine()
	if err := e.Add("a", value.MapOf("function", "test.ping", "every", "1h")); err != nil {
		t.Fatal(err)
	}
	if err := e.Add("temporary", value.MapOf("function", "test.ping", "every", "1h")); err != nil {
		t.Fatal(err)
	}
	last := time.Now().Add(-10 * time.Minute)
	e.mu.Lock()
	for _, j := range e.Jobs {
		if j.Name == "a" {
			j.LastRun = last
		}
	}
	e.mu.Unlock()

	// The files hold only `a`, so the runtime addition goes.
	reloaded, err := Parse(value.MapOf("a",
		value.MapOf("function", "test.ping", "every", "1h")), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	e.Replace(reloaded)

	listed := e.List()
	if listed.Has("temporary") {
		t.Error("a runtime addition survived a reload")
	}
	e.mu.Lock()
	got := e.Jobs[0].LastRun
	e.mu.Unlock()
	if !got.Equal(last) {
		t.Errorf("a surviving job's clock restarted: %s", got)
	}
}

// What `schedule.save` writes has to parse back into the same jobs.
func TestAScheduleSnapshotRoundTripsThroughTheParser(t *testing.T) {
	e, _, _ := controlEngine()
	err := e.Add("nightly", value.MapOf(
		"function", "state.apply",
		"cron", "17 3 * * *",
		"splay", int64(900),
		"maxrunning", int64(2),
		"return_job", false,
		"catchup", true,
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetEnabled("nightly", false); err != nil {
		t.Fatal(err)
	}

	back, err := Parse(e.Snapshot(), time.UTC)
	if err != nil {
		t.Fatalf("the snapshot does not parse: %v", err)
	}
	if len(back) != 1 {
		t.Fatalf("the snapshot parsed to %d jobs", len(back))
	}
	j := back[0]
	if j.Function != "state.apply" || j.Cron == nil || j.Cron.Expr != "17 3 * * *" {
		t.Errorf("round-tripped as %+v", j)
	}
	if j.Splay != 15*time.Minute || j.MaxRunning != 2 || j.ReturnJob || !j.Catchup || j.Enabled {
		t.Errorf("the modifiers did not survive: %+v", j)
	}
}
