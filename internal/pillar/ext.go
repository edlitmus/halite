package pillar

import (
	"context"

	"github.com/edlitmus/halite/internal/value"
)

// External pillar, SPEC section 12.7.
//
// Salt's `ext_pillar` is a list of Python callables loaded off the file
// server. Here a source is a compiled-in implementation satisfying
// ExtSource, and the list in the configuration selects and configures
// them. The interface is declared here, where it is consumed, so that a
// source package depends on this one and not the other way round.

// ExtRequest is what a source is asked for.
type ExtRequest struct {
	NodeID string
	Env    string
	// Grains are the node's own grains, in full. A source that reads
	// one is reading something the node controls, and is responsible
	// for saying so.
	Grains *value.Map
	// Pillar is what the top file produced, plus whatever the sources
	// before this one contributed. Salt's external pillar can see the
	// pillar compiled so far and some sources need it.
	Pillar *value.Map
}

// ExtSource is one external pillar source.
type ExtSource interface {
	// Name is what the configuration called it, for the diagnostics.
	Name() string
	// Pillar returns what this source contributes, to be merged.
	Pillar(ctx context.Context, req ExtRequest) (*value.Map, error)
	// FailSoft reports whether a failure is a warning rather than an
	// error, which is `ext_pillar_fail: ignore` for this source.
	FailSoft() bool
}

// mergeExt runs the configured external sources and merges what they
// return.
//
// A failure is a hard error by default, rather than Salt's default of
// logging and continuing with a partial pillar. A partial pillar is
// worse than no pillar: it silently applies a state with a missing
// value, and the state has no way to tell the difference between a
// secret that is absent and a secret that failed to arrive. SPEC 12.7.
func (c *Compiler) mergeExt(out *Compiled) {
	if len(c.Config.Ext) == 0 {
		return
	}
	ctx := c.Config.Context
	if ctx == nil {
		ctx = context.Background()
	}
	req := ExtRequest{
		NodeID: c.Config.NodeID,
		Env:    c.env(),
		Grains: c.Config.Grains,
	}
	for _, src := range c.Config.Ext {
		pos := value.Pos{File: "ext_pillar:" + src.Name()}
		// Each source sees what the ones before it contributed, which
		// is the order Salt's list implies and the only reading under
		// which the list order means anything.
		req.Pillar = out.Pillar
		contributed, err := src.Pillar(ctx, req)
		if err != nil {
			out.ExtFailed = append(out.ExtFailed, src.Name())
			if src.FailSoft() {
				out.Diags.Warn(pos, "ext_pillar", src.Name(),
					"the external pillar source %q failed and ext_pillar_fail is ignore, so this node's "+
						"pillar is missing whatever it would have contributed: %v", src.Name(), err)
				continue
			}
			out.Diags.Add(pos, "ext_pillar", src.Name(),
				"the external pillar source %q failed: %v", src.Name(), err)
			continue
		}
		if contributed == nil || contributed.Len() == 0 {
			continue
		}
		out.Pillar = value.Merge(out.Pillar, contributed, value.MergeOpts{
			Strategy:   c.Config.Strategy,
			MergeLists: c.Config.MergeLists,
		}).(*value.Map)
		out.Ext = append(out.Ext, src.Name())
	}
}
