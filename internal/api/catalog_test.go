package api

import (
	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/policy"
)

// catalogued gives a test's policy the catalog halite-api's loader gives
// a real one (cmd/halite-api loadPolicy). A policy without it grants
// nothing by pattern, which is the safe reading of one nobody told which
// functions run arbitrary code (DIVERGENCE 5.564); a test that grants by
// wildcard is asking what the API would do, so it has to have what the
// API has.
func catalogued(p *policy.Policy) {
	registries := builtin.New()
	p.UseCatalog(registries.Exec.Signatures(), registries.States.Signatures())
}
