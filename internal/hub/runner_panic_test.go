package hub

import (
	"context"
	"testing"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/transport"
)

// A runner that panics is a failed run, not a record that stays running.
//
// `net/http` recovers a handler's panic for the request and for nothing else.
// CallRunner had already written the `new` event and a job record in the state
// `dispatched`; with the panic nothing wrote a `ret` event or a return, so the
// record was listed as running for ever and the caller got a dropped
// connection. DIVERGENCE 5.527.
func TestAPanickingRunnerIsAFailedRunAndItsJobIsClosed(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.server.runners().Add(RunnerModule{
		Reach: ReachHub,
		Sig:   runnerSig("test", "boom", "Panic.", "19.2", runnerOpt("x", signature.String, "", "unused")),
		Fn:    func(c *RunnerContext) (any, error) { panic("boom") },
	})
	op := l.operator(t, "ed")

	res, err := op.Runner(context.Background(), transport.RunnerRequest{Fun: "test.boom"})
	if err != nil {
		t.Fatalf("the caller was given an error and not a result: %v", err)
	}
	if res.Success || res.Error == "" {
		t.Errorf("a panic was reported as success=%v error=%q", res.Success, res.Error)
	}

	jobs, err := l.server.Jobs.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("%d jobs recorded", len(jobs))
	}
	if jobs[0].State == job.Dispatched {
		t.Errorf("the job of a runner that panicked is still %q", jobs[0].State)
	}
	events := readEvents(t, op, []string{"halite/run/*/ret"})
	if len(events) != 1 || events[0].Data["success"] != false {
		t.Errorf("the `ret` event of the panicked run: %v", events)
	}
}
