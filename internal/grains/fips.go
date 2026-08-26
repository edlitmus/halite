package grains

import (
	"github.com/edlitmus/halite/internal/fips"
	"github.com/edlitmus/halite/internal/value"
)

// collectFIPSBuild records what this binary's own cryptography is doing,
// beside the `fips_mode` grain that reports the host kernel's state.
//
// SPEC 27.4 asks the grain to report both. They are kept as separate
// grains rather than folded into one, because `fips_mode` is a boolean
// that trees target on and templates branch on — it is in SPEC 12.4's
// default `pillar_trusted_grains` — and a map there would be truthy on
// every host, quietly inverting `{% if grains.fips_mode %}` everywhere
// it is used. DIVERGENCE 1.11 records the choice.
//
// The pair is what makes a mismatch visible: a FIPS kernel running a
// non-FIPS binary is the deployment mistake this estate has to be able
// to find, and neither fact finds it alone.
func collectFIPSBuild(g *value.Map) {
	g.Set("fips_build", fips.Artifact())
	g.Set("fips_enabled", fips.Enabled())
	g.Set("fips_module", fips.Module())
}
