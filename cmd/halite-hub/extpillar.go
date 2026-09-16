package main

import (
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/extpillar"
	"github.com/edlitmus/halite/internal/pillar"
)

// extPillarSources builds the external pillar sources of SPEC 12.7 from
// `ext_pillar`.
//
// The list keeps Salt's shape — single-key mappings, the key naming the
// source — because that is what an existing configuration holds and
// there is no value in churning it. What changed is underneath: a source
// is a signed, pinned extension of kind `pillar` rather than a Python
// file the host imports, so a name with no extension behind it is
// refused at startup instead of being loaded from whatever happens to
// be on the file server.
//
// Fatal rather than a warning, throughout. A hub that starts without a
// source its configuration names goes on to serve every node a pillar
// missing whatever that source held, which is the partial pillar SPEC
// 12.7 spends a paragraph refusing.
func extPillarSources(h *hubContext, runtime *extension.Runtime) []pillar.ExtSource {
	raw, ok := h.cfg.Get("ext_pillar")
	if !ok || raw == nil {
		return nil
	}
	specs, err := extpillar.ParseList(raw, h.cfg.String("ext_pillar_fail", "hard") == "ignore")
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if len(specs) == 0 {
		return nil
	}

	// Every string an external source returns is a secret as far as
	// this hub can tell -- `aws_secrets_manager` exists to fetch them --
	// and nothing was recording them. DIVERGENCE 5.110.
	sources, err := extpillar.Sources(specs, runtime, h.secrets.Add)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	// Started here so that a bundle whose executable will not run is a
	// hub that does not start, rather than a hub that fails the first
	// node's pillar. The handshake is also what proves the extension
	// provides `ext_pillar` at all.
	for _, spec := range specs {
		loaded, _ := runtime.Get(spec.Name)
		if err := h.warmExtension(loaded); err != nil {
			cli.Fatalf("the external pillar source %q did not start: %v", spec.Name, err)
		}
		if !provides(loaded, extpillar.EntryPoint) {
			cli.Fatalf("the %q extension is kind `pillar` and does not provide %s(); "+
				"an external pillar source is asked for that function and nothing else",
				spec.Name, extpillar.EntryPoint)
		}
		h.log.Info("external pillar source ready",
			"source", spec.Name,
			"version", loaded.Bundle.Manifest.Version,
			"fail", failWord(spec.Ignore),
			"section", "12.7")
	}
	return sources
}

// provides reports whether a loaded extension declared a function at
// handshake.
func provides(loaded *extension.Loaded, name string) bool {
	for _, sig := range loaded.Functions {
		if sig.Function == name {
			return true
		}
	}
	return false
}

func failWord(ignore bool) string {
	if ignore {
		return "ignore"
	}
	return "hard"
}
