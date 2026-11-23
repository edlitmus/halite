package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// runnerPollInterval is how often a waited-for runner call's job is read.
// A second, as `halite-hub orch run` reads it: a read writes nothing on
// the hub, and a second is how late the answer may be.
var runnerPollInterval = time.Second

// runnerGiveUpAfter is how long the hub may fail to answer, poll after
// poll, before a wait is abandoned. `orch run`'s minute.
var runnerGiveUpAfter = time.Minute

// callRunner makes a runner call to the hub, and waits for the outcome of
// one whose work can outlast a request.
//
// This service reaches the hub through a client that gives each request
// thirty seconds (cmd/halite-api), and it waited for an orchestration on
// the one request that started it. A run longer than that -- `POST
// /v1/orch`, or `state.orchestrate` through `/v1/run`'s runner client --
// was answered 502 while it went on on the hub, and the caller, told it
// had failed, had no jid to look at (issue 822; 815 is the same in
// `halite-hub orch run`).
//
// So a runner that takes `async` is started with it, and its call's job is
// polled, each poll an ordinary request, as `orch run` does it
// (transport.AwaitRunnerReturn). The answer is then what the synchronous
// call would have been, in the same shape, so no API caller has to change:
// `/v1/run` is documented to wait, and does. What bounds the wait is the
// caller's own request -- this server has no write timeout, for the event
// stream's sake -- so a caller that gives up stops the waiting and not the
// run. A caller that names `async` itself gets its jid at once, as before.
//
// Polled as this service: the hub holds reading a runner call's job to the
// grant that started it, and that was this service's certificate, acting
// for the caller this request has already been authorized as.
func (s *Server) callRunner(ctx context.Context, req transport.RunnerRequest) (*transport.RunnerResponse, error) {
	if req.Kwarg == nil {
		req.Kwarg = map[string]any{}
	}
	wait := transport.WaitsForRunner(req.Fun, req.Kwarg)
	res, err := s.Hub.Runner(ctx, req)
	if err != nil || !wait {
		return res, err
	}
	jid := transport.StartedRun(res)
	if jid == "" {
		return res, nil
	}
	ret, err := s.Hub.AwaitRunnerReturn(ctx, res.JID, runnerPollInterval, runnerGiveUpAfter)
	if err != nil {
		return nil, &runWaitError{jid: jid, err: err}
	}
	out := &transport.RunnerResponse{JID: res.JID, Fun: res.Fun, Success: ret.Success, Return: ret.Return}
	if !ret.Success {
		// The job keeps the run's timeline and not the hub's sentence
		// about it; a run that never produced a timeline is kept as its
		// error text instead.
		out.Error = ret.FailureText()
		if out.Error == "" {
			out.Error = fmt.Sprintf("orchestration %s failed; GET %s%s has the timeline", jid, PathOrchJob, jid)
		} else {
			out.Return = nil
		}
	}
	return out, nil
}

// runWaitError is a wait for a started run that did not see its outcome.
// The run is named in every case, because it is the one thing the caller
// can act on.
type runWaitError struct {
	jid string
	err error
}

func (e *runWaitError) Error() string {
	return fmt.Sprintf("waiting for orchestration %s: %v", e.jid, e.err)
}

func (e *runWaitError) Unwrap() error { return e.err }

// callerMessage is what the API caller is told. Not the wrapped error,
// which can carry the hub's address (see hubError).
func (e *runWaitError) callerMessage() string {
	switch {
	case errors.Is(e.err, transport.ErrRunnerAborted):
		return fmt.Sprintf("the hub stopped while orchestration %s was running, and the run stopped with it; "+
			"GET %s%s has how far it got", e.jid, PathOrchJob, e.jid)
	case errors.Is(e.err, transport.ErrStoppedWaiting):
		return fmt.Sprintf("stopped waiting; orchestration %s is still running on the hub, and GET %s%s has it",
			e.jid, PathOrchJob, e.jid)
	default:
		return fmt.Sprintf("the hub stopped answering while orchestration %s was running, and it may still be; "+
			"GET %s%s has it; the API's log has the detail", e.jid, PathOrchJob, e.jid)
	}
}
