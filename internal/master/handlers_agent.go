package master

import (
	"fmt"
	"net/http"
	"time"

	"github.com/edlitmus/halite/internal/archive"
	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/event"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/returner"
	"github.com/edlitmus/halite/internal/transport"
)

// handleEnroll is the only endpoint reachable without a certificate. It
// files a signing request and reports where it stands; the certificate
// comes back on a later poll, once an operator has accepted it.
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, transport.MaxBodyBytes)

	var req transport.EnrollRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	state, err := s.ca.Submit(req.ID, []byte(req.CSR))
	if err != nil {
		s.log.Printf("enrollment refused for %q from %s: %v", req.ID, r.RemoteAddr, err)
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if state == ca.StatePending && s.cfg.AutoAccept {
		if _, err := s.ca.Accept(req.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "%v", err)
			return
		}
		s.log.Printf("auto-accepted %q from %s", req.ID, r.RemoteAddr)
		state = ca.StateAccepted
	}
	s.bus.Emit(fmt.Sprintf("halite/key/%s/%s", req.ID, state), event.SourceMaster,
		map[string]any{"id": req.ID, "state": string(state)})

	resp := transport.EnrollResponse{State: string(state)}
	if state == ca.StateAccepted {
		certPEM, err := s.ca.IssuedCert(req.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "%v", err)
			return
		}
		resp.Cert = string(certPEM)
		s.bus.Emit(fmt.Sprintf(event.TagAgentEnrolled, req.ID), event.SourceMaster,
			map[string]any{"id": req.ID})
	} else {
		s.log.Printf("enrollment %s for %q from %s", state, req.ID, r.RemoteAddr)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleHello registers an agent's facts. Targeting runs against these, so
// an agent that has never said hello is never a dispatch target.
func (s *Server) handleHello(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	var req transport.HelloRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	s.registry.touch(peer.ID, req.Grains, req.Version)
	s.log.Printf("hello from %q (halite %s)", peer.ID, req.Version)
	s.bus.Emit(fmt.Sprintf(event.TagAgentHello, peer.ID), peer.ID,
		map[string]any{"id": peer.ID, "version": req.Version})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleJobs is the agent's long poll. It returns as soon as there is work,
// and returns an empty list when the poll times out so the agent can come
// straight back.
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
	s.registry.touch(peer.ID, nil, "")

	if jobs := s.registry.take(peer.ID); len(jobs) > 0 {
		writeJSON(w, http.StatusOK, jobs)
		return
	}

	timer := time.NewTimer(s.cfg.PollTimeout)
	defer timer.Stop()
	select {
	case <-s.registry.waiter(peer.ID):
	case <-timer.C:
	case <-r.Context().Done():
		return // the agent hung up; nothing to write
	}

	jobs := s.registry.take(peer.ID)
	if jobs == nil {
		jobs = []transport.Job{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

// handleResults accepts one agent's answer to one job.
func (s *Server) handleResults(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	var res transport.JobResult
	if err := decode(r, &res); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	// The agent's identity comes from its certificate, never from the body.
	res.AgentID = peer.ID
	if err := s.registry.record(res); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	s.log.Printf("job %s: %q returned ok=%v changed=%d failed=%d",
		res.JobID, peer.ID, res.Ok, res.Changed, res.Failed)
	returned := map[string]any{
		"job_id": res.JobID, "result": res.Ok,
		"succeeded": res.Succeeded, "failed": res.Failed, "changed": res.Changed,
	}
	if res.Error != "" {
		returned["error"] = res.Error
	}
	s.bus.Emit(fmt.Sprintf(event.TagJobReturn, res.JobID, peer.ID), peer.ID, returned)
	if job, ok := s.registry.jobOf(res.JobID); ok {
		s.returners.Submit(returner.Record{Time: time.Now().UTC(), Job: job, Result: res})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePillar renders the pillar for the calling agent, using the grains
// that agent last reported.
func (s *Server) handlePillar(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
	grains, ok := s.registry.grainsOf(peer.ID)
	if !ok {
		writeError(w, http.StatusConflict, "no grains on file for %q: say hello first", peer.ID)
		return
	}
	data, err := (&pillar.Loader{Root: s.cfg.PillarRoot, Grains: grains}).Load()
	if err != nil {
		s.log.Printf("pillar for %q: %v", peer.ID, err)
		writeError(w, http.StatusInternalServerError, "pillar: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// handleStateTree streams the state tree as a tar.gz. The agent extracts it
// and runs the same loader and engine it would run masterless.
func (s *Server) handleStateTree(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
	w.Header().Set("Content-Type", "application/gzip")
	if err := archive.PackDir(s.cfg.StatesRoot, w); err != nil {
		// Headers may already be out; log it and drop the connection rather
		// than pretend the body is complete.
		s.log.Printf("state tree for %q: %v", peer.ID, err)
		panic(http.ErrAbortHandler)
	}
}
