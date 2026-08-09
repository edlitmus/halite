package master

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/event"
	"github.com/edlitmus/halite/internal/transport"
)

// handleDispatch queues a job for every online agent the target matches.
func (s *Server) handleDispatch(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	var req transport.DispatchRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.Target == "" {
		writeError(w, http.StatusBadRequest, "a target is required (use '*' for the whole fleet)")
		return
	}
	if err := validKind(req); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}

	job := transport.Job{
		ID:      newJobID(),
		Kind:    req.Kind,
		Target:  req.Target,
		SLS:     req.SLS,
		Fn:      req.Fn,
		Args:    req.Args,
		Test:    req.Test,
		Created: time.Now().UTC(),
	}
	matched := s.registry.dispatch(job)
	sort.Strings(matched)
	s.log.Printf("job %s: %s on %q dispatched to %d agent(s) by %q",
		job.ID, job.Kind, job.Target, len(matched), peer.ID)
	s.bus.Emit(fmt.Sprintf(event.TagJobDispatch, job.ID), peer.ID, map[string]any{
		"job_id": job.ID, "kind": job.Kind, "target": job.Target,
		"test": job.Test, "agents": matched,
	})
	writeJSON(w, http.StatusOK, transport.DispatchResponse{JobID: job.ID, Agents: matched})
}

// validKind rejects work an agent would not know how to run, so the error
// surfaces at dispatch rather than as a fleet-wide failure.
func validKind(req transport.DispatchRequest) error {
	switch req.Kind {
	case transport.KindHighstate, transport.KindGrains, transport.KindPillar:
		return nil
	case transport.KindApply:
		if len(req.SLS) == 0 {
			return fmt.Errorf("state.apply needs at least one sls name")
		}
		return nil
	case transport.KindCall:
		if req.Fn == "" || !strings.Contains(req.Fn, ".") {
			return fmt.Errorf("call needs a module.function")
		}
		return nil
	default:
		return fmt.Errorf("unknown job kind %q", req.Kind)
	}
}

// handleAgents lists every agent the control plane has heard from.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request, _ transport.Peer) {
	agents := s.registry.list()
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	writeJSON(w, http.StatusOK, agents)
}

// handleJobInfo returns a job and the results collected so far.
func (s *Server) handleJobInfo(w http.ResponseWriter, r *http.Request, _ transport.Peer) {
	id := strings.TrimPrefix(r.URL.Path, transport.PathJobInfo)
	if id == "" {
		writeError(w, http.StatusBadRequest, "usage: %s<job-id>", transport.PathJobInfo)
		return
	}
	info, ok := s.registry.jobInfo(id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown job %q", id)
		return
	}
	writeJSON(w, http.StatusOK, info)
}
