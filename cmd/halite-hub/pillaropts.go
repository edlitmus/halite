package main

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// pillarOptions is how this hub compiles pillar, read from its
// configuration: nil when it has no pillar roots and so compiles none.
//
// One function for `serve` and `ssh`. The agentless path compiled a
// roster target's pillar with a compiler of its own, built from the roots,
// the keyring and the trusted grains and nothing else -- no external
// pillar sources, no `salt` dispatcher, the default merge strategy,
// renderer and undefined handling whatever the hub's configuration said,
// and no check that the roots are not the hub's own key material -- so a
// roster target could be sent a different pillar from an enrolled node
// with the same grains, and never what `ext_pillar` held. DIVERGENCE
// 5.265.
//
// extensions is the runtime the external sources run in; their warm-up,
// and a refusal of a source that is not installed, happen here.
func (h *hubContext) pillarOptions(extensions *extension.Runtime) (*hub.PillarOptions, error) {
	if err := extPillarWithoutRoots(h.cfg); err != nil {
		return nil, err
	}
	roots := h.cfg.Roots("pillar_roots")
	if len(roots) == 0 {
		return nil, nil
	}
	if err := checkRootsAreNotTheHubsOwn(h, roots); err != nil {
		return nil, err
	}
	strategy, ok := value.ParseStrategy(h.cfg.String("pillar_source_merging_strategy", "smart"))
	if !ok {
		return nil, fmt.Errorf("pillar_source_merging_strategy %q is not a strategy; try smart, recurse, aggregate, or overwrite",
			h.cfg.String("pillar_source_merging_strategy", ""))
	}
	undefined := template.Strict
	if h.cfg.String("undefined", "strict") == "permissive" {
		undefined = template.Permissive
	}
	return &hub.PillarOptions{
		Roots:            fileserver.NewRoots(roots),
		TrustedGrains:    h.cfg.StringSlice("pillar_trusted_grains"),
		Strategy:         strategy,
		MergeLists:       h.cfg.Bool("pillar_merge_lists", false),
		Undefined:        undefined,
		GPG:              gpgOptionsFor(h.cfg),
		Renderer:         strings.Split(h.cfg.String("renderer", "jinja|yaml"), "|"),
		YAMLBool11:       h.cfg.OptionalBool("yaml_bool_11"),
		Nondeterministic: h.cfg.String("random_seed", "deterministic") == "nondeterministic",
		Registry:         builtin.New().Exec,
		ConfigValues:     h.cfg.Effective(config.Hub),
		Ext:              extPillarSources(h, extensions),
		OnSecret:         h.secrets.Add,
		// Each pillar file's rendered output, at debug level, for the
		// node it was compiled for. The logger's redactor scrubs it like
		// any other record. DIVERGENCE 5.261.
		OnRendered: func(nodeID, file, sls string, pipeline []string, text string) {
			h.log.Debug("rendered", "node_id", nodeID, "file", file, "sls", sls,
				"pipeline", strings.Join(pipeline, "|"), "rendered", text)
		},
	}, nil
}
