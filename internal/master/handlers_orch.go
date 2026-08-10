package master

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/orch"
	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/transport"
)

// handleOrchestrate starts an orchestration and returns immediately. The
// run itself can take minutes, so progress arrives on the event bus and
// the final outcome is fetched from PathOrchInfo — an HTTP request held
// open for a fleet-wide deploy is a request that dies to a proxy timeout.
func (s *Server) handleOrchestrate(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	var req transport.OrchestrateRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "an orchestration name is required")
		return
	}

	states, err := s.loadOrchestration(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}

	id := newJobID()
	s.registry.startOrchestration(id, req.Name, peer.ID)
	s.log.Printf("orchestration %s: %q started by %q (%d step(s))",
		id, req.Name, peer.ID, len(states))

	go s.runOrchestration(id, req, states, peer.ID)
	writeJSON(w, http.StatusOK, transport.OrchestrateResponse{
		ID: id, Name: req.Name, Steps: len(states),
	})
}

// runOrchestration executes the steps and records the outcome. It runs
// detached from the request, so it uses the server's lifetime rather than
// the caller's: an operator hanging up must not abandon a half-finished
// deploy.
func (s *Server) runOrchestration(id string, req transport.OrchestrateRequest, states []sls.State, by string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.OrchTimeout)
	defer cancel()

	runner := &orch.Runner{
		Dispatch:    s.Dispatch,
		Jobs:        s.registry.jobInfo,
		Emit:        func(tag string, data map[string]any) { s.bus.Emit(tag, orchSource, data) },
		Log:         s.log,
		StepTimeout: s.cfg.OrchStepTimeout,
		By:          orchSource,
	}
	if req.Test {
		for i := range states {
			states[i].Args["test"] = "true"
		}
	}
	outcomes := runner.Run(ctx, id, states)
	s.registry.finishOrchestration(id, outcomes)

	failed := 0
	for _, outcome := range outcomes {
		if !outcome.Ok {
			failed++
		}
	}
	s.log.Printf("orchestration %s: finished, %d step(s), %d failed", id, len(outcomes), failed)
}

// loadOrchestration reads an orchestration by name from the orchestration
// tree and checks that every state is a step.
func (s *Server) loadOrchestration(name string) ([]sls.State, error) {
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return nil, fmt.Errorf("invalid orchestration name %q", name)
	}
	loader := &sls.Loader{Root: s.cfg.OrchRoot}
	states, err := loader.LoadNames([]string{name})
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, fmt.Errorf("orchestration %q has no steps", name)
	}
	for _, state := range states {
		if state.Name() != orch.StepFunction {
			return nil, fmt.Errorf("step %q is %s; an orchestration may only use %s",
				state.ID, state.Name(), orch.StepFunction)
		}
	}
	return states, nil
}

// handleOrchInfo returns an orchestration's progress and outcome.
func (s *Server) handleOrchInfo(w http.ResponseWriter, r *http.Request, _ transport.Peer) {
	id := strings.TrimPrefix(r.URL.Path, transport.PathOrchInfo)
	if id == "" {
		writeError(w, http.StatusBadRequest, "usage: %s<id>", transport.PathOrchInfo)
		return
	}
	info, ok := s.registry.orchestration(id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown orchestration %q", id)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// defaultOrchRoot is the orchestration tree beside the state tree.
func defaultOrchRoot(statesRoot string) string {
	return filepath.Join(filepath.Dir(statesRoot), "orch")
}

// orchSource is the identity orchestration steps are dispatched under.
const orchSource = "orchestrator"

// DefaultOrchTimeout bounds a whole orchestration.
const DefaultOrchTimeout = time.Hour
