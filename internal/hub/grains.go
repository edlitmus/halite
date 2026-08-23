package hub

import (
	"errors"
	"net/http"

	"github.com/edlitmus/halite/internal/transport"
)

// grainsPush is PUT /v1/grains, SPEC 6.2's grain refresh.
//
// A node re-collects its facts on an interval and after a state run
// that could have changed one — a package installed, an interface
// added — and pushes them so that targeting does not go stale between
// reconnections. Without it a hub's idea of a node is as old as its
// last connection, and SPEC 8.3's `grain_stale_after` exists to
// annotate exactly that.
func (s *Server) grainsPush(w http.ResponseWriter, r *http.Request, nodeID string) {
	var req transport.GrainsRequest
	if err := transport.ReadJSON(w, r, transport.MaxGrainsPayload, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if len(req.Grains) == 0 {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed,
			errors.New("a grain refresh carries grains"))
		return
	}
	if err := s.nodes().Put(&NodeData{
		NodeID:   nodeID,
		Grains:   req.Grains,
		Version:  req.Version,
		LastSeen: s.now(),
	}); err != nil {
		if errors.Is(err, errNoNodeCache) {
			transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
				errors.New("this hub keeps no node data"))
			return
		}
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
	s.info("grains refreshed", "node_id", nodeID, "grains_bytes", len(req.Grains))
	s.emit("halite/node/"+nodeID+"/grains", nodeID, map[string]any{"bytes": len(req.Grains)})
	w.WriteHeader(http.StatusNoContent)
}
