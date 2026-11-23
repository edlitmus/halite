package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Waiting for a runner call whose work outlasts one request.
//
// A client bounds each request (Client.Timeout), and that is a bound on a
// request, not on the work a request asks for. An orchestration can take
// an hour. `halite-hub orch run` waited for one on the request that
// started it and gave up at thirty seconds while the run went on (issue
// 815); `halite-hub runner state.orchestrate` and halite-api's runner
// calls did the same after that was fixed (issue 822). So the runners
// whose work can be long take `async`: the hub answers once the work has
// started, and answers the call's own job when it ends. What is here is
// the caller's half, shared by every caller, so that there is one way of
// waiting rather than one per command.

// asyncRunners are the runners that take `async`. Every caller that waits
// asks this, so that they agree on which calls are waited for; the hub's
// test TestTheRunnersThatTakeAsyncAreTheOnesCallersWaitFor holds the list
// to the registry, which is the other half of that pair.
var asyncRunners = map[string]bool{
	"state.orchestrate": true,
	"state.orch":        true,
	"state.orch_resume": true,
}

// AsyncRunners names the runners that take `async`, which the callers
// wait for; it exists so that the hub's test can compare the list with
// the registry.
func AsyncRunners() []string {
	out := make([]string, 0, len(asyncRunners))
	for name := range asyncRunners {
		out = append(out, name)
	}
	return out
}

// WaitsForRunner reports whether a caller should start this call `async`
// and wait for its job, and sets `async` in kwargs when it should.
//
// Not when the caller named `async` itself, either way: a caller who
// asked for the jid at once gets it, as it always has, and one who said
// `async=false` gets the single request it asked for.
func WaitsForRunner(fun string, kwargs map[string]any) bool {
	if !asyncRunners[fun] {
		return false
	}
	if _, named := kwargs["async"]; named {
		return false
	}
	kwargs["async"] = true
	return true
}

// StartedRun is the jid of a run the hub has started and not finished,
// from an `async` call's answer, or "" when the answer is the outcome
// itself: a refusal before the first step, or a run that ended before
// the hub replied.
func StartedRun(res *RunnerResponse) string {
	if !res.Success {
		return ""
	}
	var run struct {
		JID   string `json:"jid"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(res.Return, &run); err != nil || run.State != "running" {
		return ""
	}
	return run.JID
}

// RunnerReturn is a runner call's outcome, as its job holds it.
//
// Not the answer the synchronous call would have given, quite: the hub
// keeps the return and whether it succeeded, and not its sentence about
// a failure. A runner that failed with nothing to return -- a panic -- is
// kept as its error text, a JSON string.
type RunnerReturn struct {
	Success bool            `json:"success"`
	Return  json.RawMessage `json:"return"`
}

// FailureText is the error a failed call's job holds in place of a
// return, or "" when it holds a return.
func (r *RunnerReturn) FailureText() string {
	var text string
	if r.Success || json.Unmarshal(r.Return, &text) != nil {
		return ""
	}
	return text
}

// ErrStoppedWaiting is the caller's context ending, as against a hub that
// stopped answering. The call's work is not affected by it.
var ErrStoppedWaiting = errors.New("stopped waiting")

// ErrRunnerAborted is a runner call the hub closed as aborted at startup
// (Server.AbandonRunnerJobs): the hub that was running it has gone, and
// the work with it. Without it a wait that outlived a quick restart would
// poll a job nothing will ever answer.
var ErrRunnerAborted = errors.New("the hub restarted")

// AwaitRunnerReturn polls a runner call's job until its return is filed,
// every `every`, each poll an ordinary request.
//
// It gives up when the hub has not answered for giveUpAfter, poll after
// poll: a network blip should not end a wait of hours, and a hub that
// has gone for good would otherwise be waited on for ever. A refusal ends
// it at once.
func (c *Client) AwaitRunnerReturn(ctx context.Context, jid string, every, giveUpAfter time.Duration) (*RunnerReturn, error) {
	var failingSince time.Time
	for {
		status, err := c.JobStatus(ctx, jid)
		switch {
		case ctx.Err() != nil:
			return nil, ErrStoppedWaiting
		case err != nil && Permanent(err):
			return nil, fmt.Errorf("the hub refused to say how it went: %w", err)
		case err != nil:
			if failingSince.IsZero() {
				failingSince = time.Now()
			}
			if time.Since(failingSince) > giveUpAfter {
				return nil, fmt.Errorf("the hub has not answered for %s: %w", giveUpAfter, err)
			}
		case len(status.Returns) > 0:
			var ret RunnerReturn
			if err := json.Unmarshal(status.Returns[0], &ret); err != nil {
				return nil, fmt.Errorf("the hub's answer is not readable: %w", err)
			}
			return &ret, nil
		case status.State == "aborted":
			return nil, ErrRunnerAborted
		default:
			failingSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return nil, ErrStoppedWaiting
		case <-time.After(every):
		}
	}
}
