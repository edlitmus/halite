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
	// Ext are the external pillar sources of SPEC section 12.7.
	Ext []pillar.ExtSource
	// OnSecret receives every value the hub decrypts while compiling a
	// node's pillar, for the redactor of SPEC section 26.1.
	//
	// The hub is where hub-side pillar is decrypted, so it is where the
	// redactor has to learn the values: a hub that opens a GPG block
	// and then writes a compilation warning naming what was in it has
	// redacted nothing. The node seeds its own set from the pillar it
	// receives, which covers the node's output and says nothing about
	// the hub's. DIVERGENCE 5.110.
	OnSecret func(string)
	// OnRendered receives each pillar file's rendered output, with the
	// node it was compiled for: a debug log of what the templates
	// produced, as Salt prints at debug level. DIVERGENCE 5.261.
	OnRendered func(nodeID, file, sls string, pipeline []string, text string)
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
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeNoPillar,
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
	compiled, secrets, err := s.compilePillar(nodeID, env, grains)
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
		NodeID:  nodeID,
		Env:     env,
		SLS:     compiled.SLS,
		Pillar:  encoded,
		Secrets: &secrets,
	})
}

// compilePillar assembles one node's pillar from the hub's roots, and
// answers with the values the compile decrypted besides: what the node is
// told to redact. The hub's own redactor still hears each of them first.
func (s *Server) compilePillar(nodeID, env string, grains *value.Map) (*pillar.Compiled, []string, error) {
	s.pillarCompiles.Add(1)
	out, secrets := CompilePillar(s.Pillar, nodeID, env, grains)
	for _, w := range out.Warnings {
		s.warn(w.String(), "component", "pillar", "node_id", nodeID)
	}
	// Counted here rather than beside `pillarFailure`, because a source
	// whose failure is ignored never reaches that path -- and a node
	// quietly missing a secret is the case this counter exists for.
	//
	// `With` takes label *values*, one per declared label -- this family
	// has one, `source`. It was called as `.With("source", name)`, two
	// values, and the metrics package panics on a wrong count by design,
	// so every external-pillar failure on a hub with metrics on panicked
	// the request: the node got no pillar at all, even from a source
	// configured to be ignored when it fails. DIVERGENCE 5.196.
	for _, name := range out.ExtFailed {
		s.m().pillarExtFail.With(name).Inc()
	}
	if err := out.Err(); err != nil {
		return nil, nil, err
	}
	return out, secrets, nil
}

// CompilePillar compiles one node's pillar with the hub's options, and
// answers with the values that are secret besides -- what the compile
// decrypted and what a secret external source returned -- which the node
// is told to redact. opts.OnSecret still hears each of them first.
//
// The one compilation, for every caller that compiles a pillar on the
// hub's behalf: the server for an enrolled node, and `halite-hub ssh` for
// a roster target. The agentless path built its own compiler with a
// subset of the options -- no external pillar sources, no `salt`
// dispatcher, the default merge strategy and renderer whatever the hub
// said -- so a roster target could be sent a different pillar from an
// enrolled node with the same grains, and none of what `ext_pillar`
// held. DIVERGENCE 5.267.
func CompilePillar(opts *PillarOptions, nodeID, env string, grains *value.Map) (*pillar.Compiled, []string) {
	cfg := pillarConfigFor(opts, nodeID, env, grains)
	secrets := []string{}
	seen := map[string]bool{}
	hubs := cfg.OnSecret
	cfg.OnSecret = func(v string) {
		if hubs != nil {
			hubs(v)
		}
		if !seen[v] {
			seen[v] = true
			secrets = append(secrets, v)
		}
	}
	c := &pillar.Compiler{
		Loader: opts.Roots,
		Config: cfg,
	}
	return c.Compile(), secrets
}

// pillarConfigFor turns the hub's options into the compiler's
// configuration for one node.
//
// Split out so that the seam has a test. What it is guarding against is
// a field that exists on both sides and is never assigned between them:
// `OnSecret` was declared on the compiler, wired on the node, and left
// off the hub entirely, so a hub decrypted values and told its redactor
// nothing. Nothing failed, because nothing looks at a callback that is
// never called. DIVERGENCE 5.110.
func pillarConfigFor(opts *PillarOptions, nodeID, env string, grains *value.Map) pillar.Config {
	return pillar.Config{
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
		Ext:              opts.Ext,
		OnSecret:         opts.OnSecret,
		OnRendered:       renderedFor(opts.OnRendered, nodeID),
		// Never Local: this is the hub's tree, and SPEC 12.1
		// reserves that flag for a development compilation from a
		// local root.
		Local: false,
	}
}

// renderedFor binds a hub-wide OnRendered to the node being compiled for,
// because on the hub "which node" is the first question about any pillar.
func renderedFor(hook func(nodeID, file, sls string, pipeline []string, text string), nodeID string) func(file, sls string, pipeline []string, text string) {
	if hook == nil {
		return nil
	}
	return func(file, sls string, pipeline []string, text string) {
		hook(nodeID, file, sls, pipeline, text)
	}
}
