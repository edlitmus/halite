package master

import (
	"net/http"

	"github.com/edlitmus/halite/internal/transport"
)

// mineHandler routes /v1/mine: agents publish with POST, and both agents
// and operators read with GET.
//
// Agents can read the whole mine, which is the point — a load balancer's
// state needs the backends' addresses. It also means any enrolled host can
// see the facts every other host publishes, so publish facts, not secrets.
func (s *Server) mineHandler() http.Handler {
	return s.agentOnly(func(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
		if r.Method == http.MethodPost {
			s.handleMineReport(w, r, peer)
			return
		}
		s.handleMineRead(w, r)
	})
}

// handleMineReport records one function's output for the calling agent.
func (s *Server) handleMineReport(w http.ResponseWriter, r *http.Request, peer transport.Peer) {
	var report transport.MineReport
	if err := decode(r, &report); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if report.Function == "" {
		writeError(w, http.StatusBadRequest, "a mine report needs a function")
		return
	}
	// The agent it is stored under comes from the certificate, so no host
	// can publish facts on another's behalf.
	s.registry.storeMine(peer.ID, report.Function, report.Data)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMineRead returns the mine, narrowed by ?fn= and ?target=.
func (s *Server) handleMineRead(w http.ResponseWriter, r *http.Request) {
	function := r.URL.Query().Get("fn")
	target := r.URL.Query().Get("target")
	writeJSON(w, http.StatusOK, s.registry.readMine(function, target))
}
