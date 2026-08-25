package roster

import (
	"github.com/edlitmus/halite/internal/value"
)

// KnownNode is what the hub knows about a node it has heard from.
//
// Defined here, on the consumer, so this package does not depend on the
// hub's node cache.
type KnownNode struct {
	ID     string
	Grains *value.Map
}

// FromCache builds a roster from the hub's known nodes.
//
// SPEC 21.2's `cache` backend. The use is a fleet where most machines
// run the agent and a few cannot: the same names, targeted the same
// way, reached over ssh instead. The grains the node reported are
// attached, so targeting on a grain works before anything has been run.
func FromCache(nodes []KnownNode) *Roster {
	out := &Roster{}
	for _, node := range nodes {
		target := Target{ID: node.ID, Grains: node.Grains}
		// The host comes from the grains when the node reported one,
		// because a node's id is not always resolvable from the hub.
		if node.Grains != nil {
			for _, key := range []string{"fqdn", "host", "nodename"} {
				if v, ok := node.Grains.Get(key); ok {
					if host := value.KeyString(v); host != "" {
						target.Host = host
						break
					}
				}
			}
		}
		target.applyDefaults()
		out.Targets = append(out.Targets, target)
	}
	return out
}
