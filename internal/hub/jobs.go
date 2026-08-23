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
	if err := transport.ReadJSON(w, r, transport.MaxRequestBody, &ret); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if ret.NodeID != "" && ret.NodeID != nodeID {
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("the certificate says %s and the return says %s", nodeID, ret.NodeID))
		return
	}
	ret.NodeID = nodeID
	if ret.Schema == "" {
		ret.Schema = job.ReturnSchema
	}
	if s.Jobs == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub has no job cache, so a return cannot be recorded"))
		return
	}

	// A pong is a node answering a ping, per SPEC 6.2, and is not a
	// job return: it has no jid to file against.
	if ret.Fun == "pong" && ret.JID == "" {
		s.fleet().sawPong(nodeID, s.now())
		w.WriteHeader(http.StatusNoContent)
		return
	}

	fresh, err := s.Jobs.AddReturn(&ret)
	if errors.Is(err, job.ErrNoJob) {
		// A node returning against a job this hub never dispatched is
		// either a replay or a node talking to the wrong hub. Both are
		// worth a line in the log.
		s.warn("a return arrived for an unknown job", "node_id", nodeID, "jid", string(ret.JID))
		transport.WriteError(w, http.StatusNotFound, transport.CodeRefused, err)
		return
	}
	if err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if fresh {
		s.info("return recorded",
			"jid", string(ret.JID), "node_id", nodeID, "fun", ret.Fun,
			"success", ret.Success, "retcode", ret.RetCode, "duration_ms", ret.DurationMS)
		// The chain the job belongs to travels with its returns, so a
		// reactor watching a state result can tell what caused the run
		// it is looking at. A job the hub no longer has a record of
		// carries none, which is the honest answer.
		chain := ""
		if j, err := s.Jobs.Get(ret.JID); err == nil {
			chain = j.Correlation
		}
		s.emitCorrelated(tagJobRet(string(ret.JID), nodeID), nodeID, chain, map[string]any{
			"jid": string(ret.JID), "fun": ret.Fun,
			"success": ret.Success, "retcode": ret.RetCode, "duration_ms": ret.DurationMS,
		})
		// A state run gets its own tag as well, because that is what a
		// reactor watches: SPEC 17.1's halite/state/<jid>/<node>/<result>.
		if ret.Out == "highstate" {
			result := "failed"
			if ret.Success {
				result = "ok"
			}
			s.emitCorrelated(tagState(string(ret.JID), nodeID, result), nodeID, chain, map[string]any{
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
	j, err := s.Jobs.Get(id)
	if err != nil || j.State == job.Complete {
		return
	}
	j.State = job.Complete
	if err := s.Jobs.Put(j); err != nil {
		s.warn("could not mark a job complete", "jid", string(id), "error", err.Error())
	}
}

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

	// SPEC 9.1 step 2, and SPEC 23.5: every decision is logged, allowed
	// or denied, with the rule that matched. A denial that is not
	// recorded is one nobody can explain afterwards.
	decision := s.Policy.Authorize(policy.Request{
		Principal: principal,
		Target:    req.Target,
		Fun:       req.Fun,
		Arg:       req.Arg,
		Kwarg:     req.Kwarg,
	})
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
		BatchSpec:  req.Batch,
		Subset:     req.Subset,
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
		Principal: principal,
		Target:    existing.Target,
		Fun:       existing.Fun,
		Arg:       existing.Arg,
		Kwarg:     existing.Kwarg,
	})
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
	decision := s.Policy.Authorize(policy.Request{
		Principal: principal, Target: j.Target, Fun: j.Fun, Arg: j.Arg, Kwarg: j.Kwarg,
	})
	if !decision.Allowed {
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("%s", decision.Reason))
		return
	}

	res := transport.KillResponse{JID: string(id), Unqueued: append([]string(nil), j.Queued...)}
	// Expiring it is what stops the batch goroutine from advancing and
	// what makes every node refuse it: the check is already there in
	// the replay guard, and one mechanism is better than two.
	j.Queued = nil
	j.Expires = s.now().Add(-time.Second)
	j.State = job.Aborted
	if err := s.Jobs.Put(j); err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
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
