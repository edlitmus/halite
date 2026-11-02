package main

import (
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// A module that panics gives a failed return, through the executor as well
// as straight from executeJob, and the next job still runs. The deferred
// recover filled in a return the function then did not return, so the
// executor read a nil one and the node stopped. DIVERGENCE 5.542.
func TestAModuleThatPanicsIsAFailedReturnAndNotAStoppedNode(t *testing.T) {
	n := nodeWithBrokenPillar(t)
	n.registry.Exec.Add(exec.Module{
		Sig: signature.Signature{Module: "scratch", Function: "boom", Doc: "panics"},
		Fn:  func(c *exec.Context, a *value.Map) (any, error) { panic("module bug") },
	})

	ret := n.executeJob(&job.Job{JID: job.ID("20261102T1"), Fun: "scratch.boom"})
	if ret == nil {
		t.Fatal("executeJob returned nil for a job whose module panicked")
	}
	if ret.Success || !strings.Contains(string(ret.Return), "panicked") {
		t.Errorf("the return is success=%v %s, want a failure that says the job panicked", ret.Success, ret.Return)
	}

	finished := make(chan *job.Return, 2)
	e := newExecutor(n, 4, func(r *job.Return) { finished <- r })
	stop := make(chan struct{})
	defer close(stop)
	go e.Run(stop)
	e.queue <- &job.Job{JID: job.ID("20261102T2"), Fun: "scratch.boom"}
	e.queue <- &job.Job{JID: job.ID("20261102T3"), Fun: "test.ping"}
	var got []*job.Return
	for len(got) < 2 {
		select {
		case r := <-finished:
			got = append(got, r)
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of 2 jobs finished: the executor stopped", len(got))
		}
	}
	if got[0].Success || !got[1].Success {
		t.Errorf("the panicking job succeeded=%v and the next one %v, want false and true", got[0].Success, got[1].Success)
	}
}
