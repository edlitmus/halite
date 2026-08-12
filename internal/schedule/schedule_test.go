package schedule

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schedule.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingConfigIsNotAnError(t *testing.T) {
	jobs, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("an agent with no schedule is the normal case: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("want no jobs, got %v", jobs)
	}
	if jobs, err := Load(""); err != nil || jobs != nil {
		t.Fatal("no path configured means no schedule")
	}
}

func TestScheduleIsRead(t *testing.T) {
	path := writeConfig(t, ""+
		"converge:\n"+
		"  kind: highstate\n"+
		"  interval: 30m\n"+
		"  splay: 5m\n"+
		"  at_start: true\n"+
		"nightly:\n"+
		"  kind: apply\n"+
		"  sls:\n"+
		"    - web.tls\n"+
		"    - web.nginx\n"+
		"  interval: 24h\n"+
		"  test: true\n"+
		"disk:\n"+
		"  kind: call\n"+
		"  fn: disk.usage\n"+
		"  interval: 5m\n"+
		"  args:\n"+
		"    path: /var\n")

	jobs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 {
		t.Fatalf("want three jobs, got %d", len(jobs))
	}
	if jobs[0].Name != "converge" || jobs[0].Every != 30*time.Minute || jobs[0].Splay != 5*time.Minute {
		t.Fatalf("unexpected first job: %+v", jobs[0])
	}
	if !jobs[0].AtStart {
		t.Fatal("at_start should be read")
	}
	if len(jobs[1].SLS) != 2 || jobs[1].SLS[1] != "web.nginx" || !jobs[1].Test {
		t.Fatalf("unexpected apply job: %+v", jobs[1])
	}
	if jobs[2].Fn != "disk.usage" || jobs[2].Args["path"] != "/var" {
		t.Fatalf("unexpected call job: %+v", jobs[2])
	}
}

func TestJobKindDefaultsToHighstate(t *testing.T) {
	jobs, err := Load(writeConfig(t, "converge:\n  interval: 30m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].Kind != "state.highstate" {
		t.Fatalf("want a highstate, got %q", jobs[0].Kind)
	}
}

func TestUnrunnableJobsAreRefused(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no interval", "converge:\n  kind: highstate\n"},
		{"unparsable interval", "converge:\n  interval: half an hour\n"},
		{"negative interval", "converge:\n  interval: -5m\n"},
		{"splay longer than the interval", "converge:\n  interval: 5m\n  splay: 10m\n"},
		{"unknown kind", "converge:\n  kind: orchestrate\n  interval: 5m\n"},
		{"apply with no sls", "converge:\n  kind: apply\n  interval: 5m\n"},
		{"call with no function", "converge:\n  kind: call\n  interval: 5m\n"},
		{"not a mapping", "converge: 30m\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, tc.body)); err == nil {
				t.Fatal("a job that could never run should be refused at load")
			}
		})
	}
}

func TestJobsFireOnTheirInterval(t *testing.T) {
	var mu sync.Mutex
	fired := map[string]int{}
	runner := NewRunner([]Job{
		{Name: "fast", Kind: "state.highstate", Every: 10 * time.Millisecond},
		{Name: "slow", Kind: "state.highstate", Every: time.Hour},
	}, func(_ context.Context, job Job) {
		mu.Lock()
		defer mu.Unlock()
		fired[job.Name]++
	}, log.New(io.Discard, "", 0))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	runner.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	if fired["fast"] < 2 {
		t.Fatalf("want repeated runs, got %d", fired["fast"])
	}
	if fired["slow"] != 0 {
		t.Fatalf("an hourly job should not have fired yet, got %d", fired["slow"])
	}
}

func TestAtStartRunsBeforeTheFirstInterval(t *testing.T) {
	done := make(chan struct{})
	runner := NewRunner([]Job{
		{Name: "converge", Kind: "state.highstate", Every: time.Hour, AtStart: true},
	}, func(context.Context, Job) { close(done) }, log.New(io.Discard, "", 0))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go runner.Run(ctx)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("at_start should run without waiting for the interval")
	}
}

func TestCancellationStopsTheRunner(t *testing.T) {
	runner := NewRunner([]Job{
		{Name: "converge", Kind: "state.highstate", Every: time.Millisecond, Splay: time.Hour},
	}, func(context.Context, Job) { t.Error("a splayed job should not fire after cancellation") },
		log.New(io.Discard, "", 0))

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(stopped)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Run should return when its context is cancelled")
	}
}

func TestSplayStaysInsideTheInterval(t *testing.T) {
	for i := 0; i < 100; i++ {
		if got := splayOffset(time.Minute); got < 0 || got >= time.Minute {
			t.Fatalf("splay %s is outside [0, 1m)", got)
		}
	}
	if got := splayOffset(0); got != 0 {
		t.Fatalf("no splay configured means no delay, got %s", got)
	}
}
