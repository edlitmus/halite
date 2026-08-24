package hub

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// PillarOptions is what the hub needs to compile pillar for a node.
//
// It is everything the node would have used locally, taken from the
// hub's configuration instead: the point of hub-side pillar is that the
// node holds none of it and cannot see another node's.
type PillarOptions struct {
	Roots            *fileserver.Roots
	TrustedGrains    []string
	Strategy         value.Strategy
	MergeLists       bool
	Undefined        template.UndefinedMode
	GPG              render.GPGOptions
	Renderer         []string
	YAMLBool11       *bool
	Nondeterministic bool
	TemplateOptions  *template.Options
	// Registry lets `salt['pillar.get']` and its neighbours resolve
	// inside a pillar file, as they do on a node.
	Registry *exec.Registry
	// ConfigValues is what a template sees as `opts`, redacted.
	ConfigValues *value.Map
}

// pillarRequest is POST /v1/pillar: the node sends its grains and the
// hub answers with the pillar compiled for it.
//
// The identity is the certificate's, never the body's. A node asking
// for another node's pillar is the whole reason pillar compilation
// belongs on the hub rather than on the node.
func (s *Server) pillarFor(w http.ResponseWriter, r *http.Request, nodeID string) {
	var req transport.PillarRequest
	if err := transport.ReadJSON(w, r, transport.MaxGrainsPayload, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if req.NodeID != "" && req.NodeID != nodeID {
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("the certificate says %s and the request says %s", nodeID, req.NodeID))
		return
	}
	if s.Pillar == nil || s.Pillar.Roots == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub compiles no pillar; set pillar_roots"))
		return
	}

	grains := value.NewMap(0)
	if len(req.Grains) > 0 {
		decoded, err := value.DecodeJSON(req.Grains)
		if err != nil {
			transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed,
				fmt.Errorf("the grains are not readable: %w", err))
			return
		}
		if m, ok := decoded.(*value.Map); ok {
			grains = m
		}
	}
	env := req.Env
	if env == "" {
		env = "base"
	}

	started := s.now()
	compiled, err := s.compilePillar(nodeID, env, grains)
	observeSeconds(s.m().pillarCompile, s.now().Sub(started))
	if err != nil {
		s.m().pillarFailure.Inc()
		// The reason goes to the hub's log with the node named. The
		// node is told that its pillar did not compile, and not what
		// is in the file that failed: SPEC 12.7 is explicit that a
		// partial pillar is worse than no pillar, and a diagnostic
		// from someone else's tree is not this node's business.
		s.warn("pillar did not compile", "node_id", nodeID, "env", env, "error", err.Error())
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal,
			errors.New("the hub could not compile this node's pillar; its log says why"))
		return
	}

	encoded, err := value.EncodeJSON(compiled.Pillar, 0)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
	s.info("pillar compiled", "node_id", nodeID, "env", env,
		"sls", len(compiled.SLS), "keys", compiled.Pillar.Len())
	transport.WriteJSON(w, http.StatusOK, transport.PillarResponse{
		NodeID: nodeID,
		Env:    env,
		SLS:    compiled.SLS,
		Pillar: encoded,
	})
}

// compilePillar assembles one node's pillar from the hub's roots.
func (s *Server) compilePillar(nodeID, env string, grains *value.Map) (*pillar.Compiled, error) {
	opts := s.Pillar
	c := &pillar.Compiler{
		Loader: opts.Roots,
		Config: pillar.Config{
			NewSalt: func(partial *value.Map) template.Dispatcher {
				if opts.Registry == nil {
					return nil
				}
				return exec.TemplateDispatcher{
					Registry: opts.Registry,
					Context: &exec.Context{
						Grains: grains,
						Pillar: partial,
						NodeID: nodeID,
						Env:    env,
						Config: opts.ConfigValues,
					},
				}
			},
			Env:              env,
			NodeID:           nodeID,
			Grains:           grains,
			ConfigValues:     opts.ConfigValues,
			TrustedGrains:    opts.TrustedGrains,
			Strategy:         opts.Strategy,
			MergeLists:       opts.MergeLists,
			Undefined:        opts.Undefined,
			GPG:              opts.GPG,
			Renderer:         opts.Renderer,
			YAMLBool11:       opts.YAMLBool11,
			Nondeterministic: opts.Nondeterministic,
			TemplateOptions:  opts.TemplateOptions,
			// Never Local: this is the hub's tree, and SPEC 12.1
			// reserves that flag for a development compilation from a
			// local root.
			Local: false,
		},
	}
	out := c.Compile()
	for _, w := range out.Warnings {
		s.warn(w.String(), "component", "pillar", "node_id", nodeID)
	}
	if err := out.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
