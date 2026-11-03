package hub

import (
	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/policy"
)

// catalogued gives a test's policy the catalog the hub's loader gives a
// real one (cmd/halite-hub applyCatalog). A policy without it grants
// nothing by pattern, which is the safe reading of one nobody told
// which functions run arbitrary code (DIVERGENCE 5.564); a test that
// grants by wildcard is asking what the hub would do, so it has to have
// what the hub has.
func catalogued(p *policy.Policy) {
	registries := builtin.New()
	p.UseCatalog(registries.Exec.Signatures(), registries.States.Signatures())
}
