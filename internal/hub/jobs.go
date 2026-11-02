package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// returned is POST /v1/return: one node's answer to one job.
//
// The node identity comes from the certificate, so a node cannot file a
// return on another's behalf however it fills in the body.
func (s *Server) returned(w http.ResponseWriter, r *http.Request, nodeID string) {
	var ret job.Return
	// SPEC 6.5's return bound, which was written down and not enforced:
	// this read MaxRequestBody, 64 MiB, and a node was sent the job, so
	// it could file that much against every job it got (issue 321,
	// M-13).
	if err := transport.ReadJSON(w, r, transport.MaxReturnBody, &ret); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if ret.NodeID != "" && ret.NodeID != nodeID {
		// A relay files returns for the nodes it proxies for, and for
		// nobody else. Without that second half a relay could file a
		// return for any node in the estate — which is a job that
		// looks like it succeeded on a machine it never reached.
		if !s.relayMayReturn(nodeID, ret.NodeID) {
			transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
				fmt.Errorf("the certificate says %s and the return says %s", nodeID, ret.NodeID))
			return
		}
	}
	if ret.NodeID == "" {
		ret.NodeID = nodeID
	}
	// Attribution is the node that ran the job, not the certificate the
	// return arrived on. The two differ behind a relay, and using the
	// certificate tagged every relayed return with the relay's own id:
	// a reactor watching halite/job/<jid>/ret/web1.example never fired,
	// because upstream every one of them said ret/relay1.example.
	ran := ret.NodeID
	// A return with no schema is one from a node old enough not to send
	// one, and is this build's shape: stamped and accepted.
	//
	// A return with a schema this build does not know is **accepted and
	// said so**, which is the opposite of what the job record does two
	// paragraphs of reasoning away, and deliberately. A record is
	// bookkeeping the hub owns and can decline to touch; a return is
	// the only evidence that work already happened on a node. Refusing
	// it loses that evidence and the node has nowhere to put it again,
	// so the job would look unanswered for ever. The same argument
	// `doctor`'s disk-full check makes: a write that fails after the
	// instruction has gone out must not be turned into a second
	// untruth.
	//
	// What it must not be is silent. SPEC 9.4 freezes `halite.ret/1`
	// "so a dashboard built on it keeps working", and until this the
	// freeze had nothing behind it — an unknown schema was stored
	// verbatim and nothing anywhere noticed.
	switch ret.Schema {
	case "":
		ret.Schema = job.ReturnSchema
	case job.ReturnSchema:
	default:
		s.m().returnsForeignSchema.Inc()
		s.warn("a return arrived with a schema this build does not know",
			"jid", string(ret.JID), "node_id", ret.NodeID,
			"schema", ret.Schema, "known", job.ReturnSchema,
			"node_version", ret.NodeVersion)
	}
	if s.Jobs == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub has no job cache, so a return cannot be recorded"))
		return
	}

	// A pong is a node answering a ping, per SPEC 6.2, and is not a
	// job return: it has no jid to file against. Nothing records it.
	// SPEC 6.2 puts the missed-ping reconnect on the node, and the hub
	// learns a peer is gone when a ping write fails (subscribe.go); the
	// timestamp this used to keep was never read. DIVERGENCE 5.227.
	if ret.Fun == "pong" && ret.JID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	fresh, err := s.Jobs.AddReturn(&ret)
	if errors.Is(err, job.ErrNoJob) {
		// A node returning against a job this hub never dispatched is
		// either a replay or a node talking to the wrong hub. Both are
		// worth a line in the log.
		s.warn("a return arrived for an unknown job", "node_id", ran, "jid", string(ret.JID))
		transport.WriteError(w, http.StatusNotFound, transport.CodeRefused, err)
		return
	}
	if errors.Is(err, job.ErrNotTargeted) {
		s.warn("a return arrived from a node the job was not sent to",
			"node_id", ran, "jid", string(ret.JID), "certificate", nodeID)
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused, err)
		return
	}
	if err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if fresh {
		// The function the job was submitted with, from the record; the
		// return's own `fun` is a node's text (DIVERGENCE 5.532).
		recorded := ""
		if j, err := s.Jobs.Get(ret.JID); err == nil {
			recorded = j.Fun
		}
		s.countReturn(&ret, recorded)
		if s.OnReturn != nil {
			// A relay forwards it upstream. In the background, because
			// the node is waiting on this response and an upstream
			// that is slow must not make every subordinate's return
			// slow too.
			forwarded := ret
			s.goBackground(func() { s.OnReturn(&forwarded) })
		}
		s.info("return recorded",
			"jid", string(ret.JID), "node_id", ran, "fun", ret.Fun,
			"success", ret.Success, "retcode", ret.RetCode, "duration_ms", ret.DurationMS)
		// The chain the job belongs to travels with its returns, so a
		// reactor watching a state result can tell what caused the run
		// it is looking at. A job the hub no longer has a record of
		// carries none, which is the honest answer.
		chain, ranAState := "", false
		if j, err := s.Jobs.Get(ret.JID); err == nil {
			chain = j.Correlation
			ranAState = appliesState(j.Fun)
		}
		s.emitCorrelated(tagJobRet(string(ret.JID), ran), ran, chain, map[string]any{
			"jid": string(ret.JID), "fun": ret.Fun,
			"success": ret.Success, "retcode": ret.RetCode, "duration_ms": ret.DurationMS,
		})
		// A state run gets its own tag as well, because that is what a
		// reactor watches: SPEC 17.1's halite/state/<jid>/<node>/<result>.
		//
		// Decided by the function the hub recorded for the job; the output
		// formatter the node put on its return is only a second opinion.
		// `Out` is the node's to set, and the node also uses "highstate"
		// for the `show_*` functions, which compile and run nothing: a
		// `test.ping` return that said so, or a `state.show_lowstate`,
		// published a state result for a run that never happened, to the
		// reactors and change feeds that act on it. DIVERGENCE 5.517.
		if ret.Out == "highstate" && ranAState {
			result := "failed"
			if ret.Success {
				result = "ok"
			}
			s.emitCorrelated(tagState(string(ret.JID), ran, result), ran, chain, map[string]any{
				"jid": string(ret.JID), "retcode": ret.RetCode,
			})
		}
		s.completeIfDone(ret.JID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// completeIfDone marks a job complete once every expected node has
// answered, so that `jobs list` distinguishes finished from waiting
// without re-reading every return.
func (s *Server) completeIfDone(id job.ID) {
	missing, err := s.Jobs.Missing(id)
	if err != nil || len(missing) > 0 {
		return
	}
	// Through Update rather than Get and Put: the goroutine that
	// delivers a node its queued jobs is editing this same record, and
	// a whole-record write here would put back the spool entry it had
	// just cleared -- sending the node the job a second time on its
	// next connection. See job.Cache.Update.
	_, err = s.Jobs.Update(id, func(cur *job.Job) error {
		// Not an aborted one either. An operator's kill is a decision, and
		// a node that was already running the job answering late makes
		// nothing missing, which used to read as the job having completed.
		// DIVERGENCE 5.515.
		if cur.State == job.Complete || cur.State == job.Aborted {
			return errAlreadyComplete
		}
		cur.State = job.Complete
		return nil
	})
	if err != nil && !errors.Is(err, errAlreadyComplete) {
		s.warn("could not mark a job complete", "jid", string(id), "error", err.Error())
	}
}

// errAlreadyComplete abandons an update that has nothing to do. It never
// reaches an operator: a job marked complete twice is two goroutines
// agreeing, not a fault.
var errAlreadyComplete = errors.New("the job is already complete")

// errJobFinished is what kill's update answers for a job that has completed,
// which the handler turns into a 409.
var errJobFinished = errors.New("the job has already completed")

// submit is POST /v1/jobs: an operator asking for a job.
func (s *Server) submit(w http.ResponseWriter, r *http.Request, principal string) {
	var req transport.SubmitRequest
	if err := transport.ReadJSON(w, r, transport.MaxRequestBody, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	offline, err := job.ParseOffline(req.Offline)
	if err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	// SPEC 25.6: a signed submission brings its own identifier and
	// expiry, because the signature covers both. Parsed here and
	// checked in Dispatch, which is also where an unsigned request that
	// tries to set either is refused.
	var expires time.Time
	if req.ExpiresAt != "" {
		expires, err = time.Parse(time.RFC3339Nano, req.ExpiresAt)
		if err != nil {
			transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed,
				fmt.Errorf("the expiry %q is not an RFC 3339 timestamp: %w", req.ExpiresAt, err))
			return
		}
	}

	// SPEC 9.1 step 2, and SPEC 23.5: every decision is logged, allowed
	// or denied, with the rule that matched. A denial that is not
	// recorded is one nobody can explain afterwards.
	//
	// The kind goes with the target: it is what Dispatch below compiles
	// the target under, and a check that read the string as a glob let
	// `web1.prod,db1.prod` (-L) past a `web*.prod` rule. DIVERGENCE 5.296.
	decision := s.Policy.Authorize(policy.Request{
		Principal:  principal,
		Target:     req.Target,
		TargetKind: req.TargetKind,
		Fun:        req.Fun,
		Arg:        req.Arg,
		Kwarg:      req.Kwarg,
		Test:       req.Test,
	})
	s.countDecision(decision)
	if !decision.Allowed {
		s.warn("job refused by policy",
			"principal", principal, "target", req.Target, "fun", req.Fun,
			"reason", decision.Reason)
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("%s", decision.Reason))
		return
	}
	s.info("job authorized",
		"principal", principal, "target", req.Target, "fun", req.Fun,
		"role", decision.Role, "rule", decision.RuleIndex)
	j, err := s.Dispatch(Submission{
		Target:     req.Target,
		TargetKind: req.TargetKind,
		Fun:        req.Fun,
		Arg:        req.Arg,
		Kwarg:      req.Kwarg,
		Env:        req.Env,
		Test:       req.Test,
		Offline:    offline,
		TTL:        time.Duration(req.TTLSeconds) * time.Second,
		Submitter:  principal,
		OnBehalfOf: req.OnBehalfOf,
		BatchSpec:  req.Batch,
		Subset:     req.Subset,
		JID:        job.ID(req.JID),
		Expires:    expires,
		Signature:  req.Signature,
		Batch: job.Batch{
			Wait:      time.Duration(req.BatchWaitSeconds) * time.Second,
			SafeLimit: req.BatchSafeLimit,
			Timeout:   time.Duration(req.BatchTimeoutSecs) * time.Second,
		},
	})
	if err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	connected := s.fleet().Connected()
	var absent []string
	for _, id := range j.Nodes {
		if _, up := connected[id]; !up {
			absent = append(absent, id)
		}
	}
	transport.WriteJSON(w, http.StatusAccepted, transport.SubmitResponse{
		JID:    string(j.JID),
		Nodes:  j.Nodes,
		Absent: absent,
		Batch:  j.Batch.Size,
	})
}

// resume is POST /v1/jobs/{jid}/resume: pick up a batch the hub was
// part way through when it stopped.
func (s *Server) resume(w http.ResponseWriter, r *http.Request, principal string) {
	rest := strings.TrimPrefix(r.URL.Path, transport.PathJob)
	id := job.ID(strings.TrimSuffix(rest, "/resume"))
	if !id.Valid() {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed,
			fmt.Errorf("%q is not a job identifier", id))
		return
	}
	if s.Jobs == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub has no job cache"))
		return
	}
	existing, err := s.Jobs.Get(id)
	if errors.Is(err, job.ErrNoJob) {
		transport.WriteError(w, http.StatusNotFound, transport.CodeRefused, err)
		return
	}
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
	// Resuming re-runs the original request, so it is authorized as
	// the original request: an operator who may not run it now may not
	// resume it either, whoever submitted it before.
	decision := s.Policy.Authorize(policy.Request{
		Principal:  principal,
		Target:     existing.Target,
		TargetKind: existing.TargetKind,
		Fun:        existing.Fun,
		Arg:        existing.Arg,
		Kwarg:      existing.Kwarg,
		Test:       existing.Test,
	})
	s.countDecision(decision)
	if !decision.Allowed {
		s.warn("resume refused by policy",
			"principal", principal, "jid", string(id), "reason", decision.Reason)
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("%s", decision.Reason))
		return
	}

	resumed, err := s.Resume(s.batchContext(), id)
	if err != nil {
		transport.WriteError(w, http.StatusConflict, transport.CodeRefused, err)
		return
	}
	s.info("batch resumed", "jid", string(id), "principal", principal,
		"remaining", len(resumed.Remaining()))
	transport.WriteJSON(w, http.StatusAccepted, transport.ResumeResponse{
		JID:       string(id),
		Remaining: resumed.Remaining(),
	})
}

// kill is POST /v1/jobs/{jid}/kill: stop a job that is still going.
//
// What can be stopped is what has not happened yet: a node already
// running a state cannot be interrupted mid-write, and pretending
// otherwise would be worse than saying so. A queued job is unspooled, a
// batch stops advancing, and the nodes that have it are told.
func (s *Server) kill(w http.ResponseWriter, r *http.Request, principal string) {
	rest := strings.TrimPrefix(r.URL.Path, transport.PathJob)
	id := job.ID(strings.TrimSuffix(rest, "/kill"))
	if !id.Valid() {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed,
			fmt.Errorf("%q is not a job identifier", id))
		return
	}
	if s.Jobs == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub has no job cache"))
		return
	}
	j, err := s.Jobs.Get(id)
	if errors.Is(err, job.ErrNoJob) {
		transport.WriteError(w, http.StatusNotFound, transport.CodeRefused, err)
		return
	}
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
	decision := s.Policy.Authorize(requestForJob(principal, j))
	s.countDecision(decision)
	if !decision.Allowed {
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("%s", decision.Reason))
		return
	}

	res := transport.KillResponse{JID: string(id), Unqueued: append([]string(nil), j.Queued...)}
	// Expiring it is what stops the batch goroutine from advancing and
	// what makes every node refuse it: the check is already there in
	// the replay guard, and one mechanism is better than two.
	//
	// A job that has completed has nothing left to stop. Marking it aborted
	// turned a successful run into an aborted one and told every node to
	// cancel work that was over, for an operator whose job list was a
	// minute old. It is refused rather than quietly accepted, as `resume`
	// refuses what it cannot resume; killing one that is already aborted
	// stays harmless and repeatable. DIVERGENCE 5.515.
	if _, err := s.Jobs.Update(id, func(cur *job.Job) error {
		if cur.State == job.Complete {
			return errJobFinished
		}
		cur.Queued = nil
		cur.Expires = s.now().Add(-time.Second)
		cur.State = job.Aborted
		return nil
	}); err != nil {
		if errors.Is(err, errJobFinished) {
			transport.WriteError(w, http.StatusConflict, transport.CodeRefused,
				fmt.Errorf("job %s has already completed; there is nothing to stop", id))
			return
		}
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
	// A killed job owes nothing, including from a node that was already
	// running it and may yet answer: the operator has decided it is
	// over, and an alert that kept firing for it would be telling them
	// about a decision they made. Not an expiry either, although the
	// record now carries a past one; Settle skips an aborted job, so
	// this is the only place it is closed. A second kill finds it
	// closed and moves nothing.
	s.countJobEnded(id)
	for _, node := range j.Delivered {
		if s.fleet().Send(node, transport.Message{
			T: transport.MsgKill, JID: string(id), Reason: "cancelled by " + principal,
		}) {
			res.Told = append(res.Told, node)
		}
	}
	s.warn("job killed", "jid", string(id), "principal", principal,
		"told", res.Told, "unqueued", res.Unqueued)
	s.emit("halite/job/"+string(id)+"/kill", "", map[string]any{
		"jid": string(id), "principal": principal,
		"told": res.Told, "unqueued": res.Unqueued,
	})
	transport.WriteJSON(w, http.StatusOK, res)
}

// jobStatus is GET /v1/jobs/{jid}: what has come back so far.
func (s *Server) jobStatus(w http.ResponseWriter, r *http.Request, principal string) {
	id := job.ID(strings.TrimPrefix(r.URL.Path, transport.PathJob))
	if !id.Valid() {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed,
			fmt.Errorf("%q is not a job identifier", id))
		return
	}
	if s.Jobs == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub has no job cache"))
		return
	}
	j, err := s.Jobs.Get(id)
	if errors.Is(err, job.ErrNoJob) {
		transport.WriteError(w, http.StatusNotFound, transport.CodeRefused, err)
		return
	}
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
	// Reading a job is held to the policy that governed running it. This
	// handler used to check only that the caller held an operator
	// certificate, where submit, resume and kill all authorize: an operator
	// bound to `web*` could not run anything on `db1`, and could read every
	// return `db1` had made, which is state output and so may hold secrets.
	// DIVERGENCE 5.513.
	decision := s.Policy.Authorize(requestForJob(principal, j))
	s.countDecision(decision)
	if !decision.Allowed {
		s.warn("job read refused by policy",
			"principal", principal, "jid", string(id), "reason", decision.Reason)
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("%s", decision.Reason))
		return
	}
	returns, err := s.Jobs.Returns(id)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
	missing, err := s.Jobs.Missing(id)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
	encoded := make([]json.RawMessage, 0, len(returns))
	for _, ret := range returns {
		raw, err := json.Marshal(ret)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
			return
		}
		encoded = append(encoded, raw)
	}
	transport.WriteJSON(w, http.StatusOK, transport.JobStatus{
		JID:       string(j.JID),
		Fun:       j.Fun,
		Target:    j.Target,
		State:     string(j.State),
		Nodes:     j.Nodes,
		Delivered: j.Delivered,
		Missing:   missing,
		Returns:   encoded,
	})
}

// requestForJob is the authorization request for acting on a job that is
// already recorded: reading it, or stopping it.
//
// A runner call is recorded as a job against the pseudo-target "hub", which
// a node-targeted rule says nothing about, so it is authorized the way the
// call was made: as the runner function, which is also how `metrics.show`
// is gated. `jobStatus` did this inline and `kill` did not, so an operator
// holding the runner could read a runner call and not stop it, and an
// operator holding only `*` node rules could be refused or allowed by
// whatever a glob over "hub" happened to say. DIVERGENCE 5.516.
func requestForJob(principal string, j *job.Job) policy.Request {
	if j.Target == RunnerTarget {
		return policy.Request{Principal: principal, Fun: j.Fun, Arg: j.Arg, Kwarg: j.Kwarg, Runner: true}
	}
	return policy.Request{
		Principal: principal, Target: j.Target, TargetKind: j.TargetKind,
		Fun: j.Fun, Arg: j.Arg, Kwarg: j.Kwarg, Test: j.Test,
	}
}

// appliesState reports whether a job's function runs states on the node, as
// opposed to showing what they would be. The names are the node's
// `stateFunction` set without its `show_*` forms; the two lists are in
// different binaries, which is why this is a list and not an import.
func appliesState(fun string) bool {
	switch fun {
	case "state.apply", "state.highstate", "state.sls":
		return true
	}
	return false
}
