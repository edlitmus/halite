package main

import (
	"context"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/schedule"
	"github.com/edlitmus/halite/internal/value"
)

// A scheduled job waits for a job from the hub to finish, as SPEC 9.6
// says a node's jobs do (issue 781).
//
// The schedule used to call executeJob from its own goroutine, so a
// scheduled highstate ran alongside a dispatched one. Two one-second
// sleeps: one at a time, the scheduled run ends about two seconds after
// the dispatched one began, and side by side it ends after about one.
func TestAScheduledJobDoesNotRunAlongsideADispatchedOne(t *testing.T) {
	n := nodeForEvidence(t, "")
	n.refusals = make(chan *job.Return, 4)
	dispatched := make(chan *job.Return, 1)
	n.executor = newExecutor(n, 4, func(r *job.Return) { dispatched <- r })
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go n.executor.Run(stop)

	start := time.Now()
	msg := jobMessage(t, "20261121T120000000001")
	msg.Fun = "test.sleep"
	msg.Kwarg = map[string]any{"length": int64(1)}
	n.acceptJob(msg)
	waitFor(t, "the dispatched job to start", func() bool { return n.executor.Depth() == 0 })

	sleep := schedule.Run{Job: &schedule.Job{
		Name: "nap", Function: "test.sleep", Kwargs: value.MapOf("length", int64(1)),
	}, Fire: time.Now()}
	if err := n.runScheduled(context.Background(), sleep); err != nil {
		t.Fatalf("the scheduled job failed: %v", err)
	}
	elapsed := time.Since(start)

	select {
	case ret := <-dispatched:
		if !ret.Success {
			t.Fatalf("the dispatched job failed: %s", ret.Return)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatched job never returned")
	}
	if elapsed < 1800*time.Millisecond {
		t.Errorf("both one-second jobs were done %v after the first began: they ran side by side", elapsed)
	}
}

// A cancelled job that has not started is taken out of the queue, and
// only that one: the jobs either side keep their places.
func TestCancellingAQueuedJobRemovesOnlyThatJob(t *testing.T) {
	n := nodeForEvidence(t, "")
	e := newExecutor(n, 8, func(*job.Return) {})
	for _, jid := range []string{"20261121T120000000011", "20261121T120000000012", "20261121T120000000013"} {
		if err := e.enqueue(&pending{job: &job.Job{JID: job.ID(jid), Fun: "test.ping"}, finish: e.returns}); err != nil {
			t.Fatal(err)
		}
	}

	if !e.Cancel("20261121T120000000012") {
		t.Fatal("Cancel did not find a job that was waiting")
	}
	if e.Cancel("20261121T120000000012") {
		t.Error("Cancel found the same job twice")
	}

	var left []job.ID
	for _, p := range e.waiting {
		left = append(left, p.job.JID)
	}
	if len(left) != 2 || left[0] != "20261121T120000000011" || left[1] != "20261121T120000000013" {
		t.Errorf("the queue holds %v, want the first and third in order", left)
	}
}

// A job that has started is not cancelled: a state run stopped halfway
// leaves a machine in neither state.
func TestARunningJobIsNotCancelled(t *testing.T) {
	n := nodeForEvidence(t, "")
	finished := make(chan *job.Return, 1)
	e := newExecutor(n, 4, func(r *job.Return) { finished <- r })
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go e.Run(stop)

	j := &job.Job{JID: "20261121T120000000021", Fun: "test.sleep", Kwarg: map[string]any{"length": int64(1)}}
	if err := e.enqueue(&pending{job: j, finish: e.returns}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the job to start", func() bool { return e.Depth() == 0 })

	if e.Cancel(j.JID) {
		t.Error("Cancel says it dropped a job that was already running")
	}
	select {
	case ret := <-finished:
		if !ret.Success {
			t.Errorf("the running job did not finish cleanly: %s", ret.Return)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the running job never finished")
	}
}
