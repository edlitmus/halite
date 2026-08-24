package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// RunRequest is what `/v1/run` and `POST /v1/jobs` take.
//
// The field names are Salt's netapi, deliberately: an existing client
// or CI job posts this shape, and the point of the endpoint is that it
// keeps working.
type RunRequest struct {
	// Client is Salt's netapi client type, preserved by SPEC 22.1.
	Client string `json:"client,omitempty"`
	Target string `json:"tgt,omitempty"`
	// TargetType is Salt's `tgt_type`; `expr_form` is its older name
	// and an older client still sends it.
	TargetType string         `json:"tgt_type,omitempty"`
	ExprForm   string         `json:"expr_form,omitempty"`
	Function   string         `json:"fun"`
	Arg        []string       `json:"arg,omitempty"`
	Kwarg      map[string]any `json:"kwarg,omitempty"`
	Env        string         `json:"saltenv,omitempty"`
	Test       bool           `json:"test,omitempty"`

	Batch          string `json:"batch,omitempty"`
	BatchSafeLimit int    `json:"batch_safe_limit,omitempty"`
	Subset         int    `json:"subset,omitempty"`
	// Timeout bounds how long a synchronous run gathers returns.
	Timeout string `json:"timeout,omitempty"`
	Offline string `json:"offline,omitempty"`
}

// targetKind reads whichever of the two names the client sent.
func (r *RunRequest) targetKind() string {
	if r.TargetType != "" {
		return r.TargetType
	}
	return r.ExprForm
}

// RunResponse is a synchronous run: what each node said.
type RunResponse struct {
	JID     string         `json:"jid"`
	Nodes   []string       `json:"nodes,omitempty"`
	Absent  []string       `json:"absent,omitempty"`
	Missing []string       `json:"missing,omitempty"`
	Return  map[string]any `json:"return"`
}

// JobAccepted is an asynchronous submission.
type JobAccepted struct {
	JID    string   `json:"jid"`
	Nodes  []string `json:"nodes,omitempty"`
	Absent []string `json:"absent,omitempty"`
	Batch  int      `json:"batch,omitempty"`
}

// run is `POST /v1/run`: submit and wait.
func (s *Server) run(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	s.execute(w, r, token, true)
}

// submit is `POST /v1/jobs`: submit and return the jid.
func (s *Server) submit(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	s.execute(w, r, token, false)
}

// execute is the body of both, since they differ only in whether they
// wait.
func (s *Server) execute(w http.ResponseWriter, r *http.Request, token *apitoken.Token, wait bool) {
	var req RunRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Function == "" {
		writeError(w, http.StatusBadRequest, "a run needs `fun`, the function to call")
		return
	}

	client := req.Client
	if client == "" {
		client = "local"
		if !wait {
			client = "local_async"
		}
	}
	switch client {
	case "runner", "runner_async", "wheel", "wheel_async":
		s.runRunner(w, r, token, req, client)
		return
	case "local", "local_async", "local_batch":
	default:
		writeError(w, http.StatusBadRequest, errUnknownClient(client).Error())
		return
	}

	if req.Target == "" {
		writeError(w, http.StatusBadRequest, "a local run needs `tgt`, the nodes to run against")
		return
	}
	if !s.authorize(w, token, requestFor(req.Function, req.Target, req.Arg, req.Kwarg, false)) {
		return
	}

	res, err := s.Hub.Submit(r.Context(), transport.SubmitRequest{
		Target:         req.Target,
		TargetKind:     req.targetKind(),
		Fun:            req.Function,
		Arg:            req.Arg,
		Kwarg:          req.Kwarg,
		Env:            req.Env,
		Test:           req.Test,
		Offline:        req.Offline,
		Batch:          req.Batch,
		BatchSafeLimit: req.BatchSafeLimit,
		Subset:         req.Subset,
		// Recorded so the job cache can say who really asked, without
		// the hub trusting it: the hub authorizes this service's
		// certificate and nothing in the body.
		OnBehalfOf: token.Principal,
	})
	if err != nil {
		s.hubError(w, "submitting the job", err)
		return
	}

	if !wait && client != "local" {
		writeJSON(w, http.StatusAccepted, JobAccepted{
			JID: res.JID, Nodes: res.Nodes, Absent: res.Absent, Batch: res.Batch,
		})
		return
	}
	s.gather(w, r, res, req.Timeout)
}

// gather waits for a job's returns, up to the caller's timeout.
//
// Polling the hub rather than holding a stream open: the job and its
// returns are hub-side records, so a caller that is disconnected, or
// asks again later, sees exactly the same thing. SPEC 9.3 makes the
// same argument for batching.
func (s *Server) gather(w http.ResponseWriter, r *http.Request, sub *transport.SubmitResponse, timeout string) {
	window := 5 * time.Minute
	if timeout != "" {
		d, err := time.ParseDuration(timeout)
		if err != nil {
			writeError(w, http.StatusBadRequest, "`timeout` is a duration, such as 30s")
			return
		}
		window = d
	}
	ctx, cancel := context.WithTimeout(r.Context(), window)
	defer cancel()

	// Only the nodes the job reached. Waiting the whole window for one
	// that was reported as not connected means a run against the
	// estate blocks because a single machine is off.
	expected := len(sub.Nodes) - len(sub.Absent)
	var status *transport.JobStatus
	for {
		got, err := s.Hub.JobStatus(ctx, sub.JID)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			s.hubError(w, "reading the job", err)
			return
		}
		status = got
		if (len(status.Returns) >= expected && status.State != "batching") ||
			status.State == "aborted" {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
			continue
		}
		break
	}
	if status == nil {
		writeError(w, http.StatusGatewayTimeout, "the job produced no answer in time")
		return
	}

	out := RunResponse{
		JID:     status.JID,
		Nodes:   status.Nodes,
		Absent:  sub.Absent,
		Missing: status.Missing,
		Return:  map[string]any{},
	}
	for _, raw := range status.Returns {
		var ret struct {
			NodeID string          `json:"id"`
			Return json.RawMessage `json:"return"`
		}
		if err := json.Unmarshal(raw, &ret); err != nil {
			continue
		}
		decoded, err := value.DecodeJSON(ret.Return)
		if err != nil {
			decoded = string(ret.Return)
		}
		out.Return[ret.NodeID] = decoded
	}
	writeJSON(w, http.StatusOK, out)
}

// runRunner serves the runner and wheel client types.
//
// One namespace: this build has one set of hub functions rather than
// Salt's runner and wheel, so a `wheel` client reaches the same
// registry a `runner` client does. It is accepted rather than refused
// because an existing client sends it and means the same thing.
func (s *Server) runRunner(w http.ResponseWriter, r *http.Request, token *apitoken.Token, req RunRequest, client string) {
	if !s.authorize(w, token, requestFor(req.Function, "", req.Arg, req.Kwarg, true)) {
		return
	}
	res, err := s.Hub.Runner(r.Context(), transport.RunnerRequest{
		Fun: req.Function, Arg: req.Arg, Kwarg: req.Kwarg,
	})
	if err != nil {
		s.hubError(w, "calling the runner", err)
		return
	}
	decoded, err := value.DecodeJSON(res.Return)
	if err != nil {
		decoded = string(res.Return)
	}
	status := http.StatusOK
	if !res.Success {
		// The runner ran and said no. That is an answer, and the
		// status says the request did not get what it asked for
		// without pretending the service failed.
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{
		"jid":     res.JID,
		"fun":     res.Fun,
		"success": res.Success,
		"return":  decoded,
		"error":   res.Error,
	})
}

// hubError turns a failure from the hub into an answer.
//
// A refusal from the hub is the API's own grant being too narrow, and
// saying so is the only way an operator can act on it: the alternative
// is a 500 that blames the wrong component.
func (s *Server) hubError(w http.ResponseWriter, doing string, err error) {
	s.warn("the hub refused or failed", "doing", doing, "error", err.Error())
	if transport.Permanent(err) {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeError(w, http.StatusBadGateway, doing+": "+err.Error())
}

// jobList is `GET /v1/jobs`.
func (s *Server) jobList(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	if !s.authorize(w, token, requestFor("jobs.list_jobs", "", nil, nil, true)) {
		return
	}
	kwargs := map[string]any{}
	if limit := r.URL.Query().Get("limit"); limit != "" {
		kwargs["limit"] = limit
	}
	s.forwardRunner(w, r, "jobs.list_jobs", nil, kwargs)
}

// jobDetail is `GET /v1/jobs/{jid}` and `DELETE /v1/jobs/{jid}`.
func (s *Server) jobDetail(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	jid := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
	if jid == "" || strings.Contains(jid, "/") {
		writeError(w, http.StatusBadRequest, "that is not a job identifier")
		return
	}

	if r.Method == http.MethodDelete {
		if !s.authorize(w, token, requestFor("jobs.kill", "", []string{jid}, nil, true)) {
			return
		}
		res, err := s.Hub.KillJob(r.Context(), jid)
		if err != nil {
			s.hubError(w, "killing the job", err)
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}

	if !s.authorize(w, token, requestFor("jobs.list_job", "", []string{jid}, nil, true)) {
		return
	}
	s.forwardRunner(w, r, "jobs.list_job", []string{jid}, nil)
}

// forwardRunner calls a hub runner and returns what it said.
func (s *Server) forwardRunner(w http.ResponseWriter, r *http.Request, fun string, arg []string, kwarg map[string]any) {
	res, err := s.Hub.Runner(r.Context(), transport.RunnerRequest{Fun: fun, Arg: arg, Kwarg: kwarg})
	if err != nil {
		s.hubError(w, "calling "+fun, err)
		return
	}
	decoded, err := value.DecodeJSON(res.Return)
	if err != nil {
		decoded = string(res.Return)
	}
	if !res.Success {
		writeError(w, http.StatusUnprocessableEntity, res.Error)
		return
	}
	writeJSON(w, http.StatusOK, decoded)
}
