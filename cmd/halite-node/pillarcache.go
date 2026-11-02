package main

import (
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// The longest a beacon poll reuses a compiled pillar, and the shortest it
// waits before asking again after a compilation failed.
//
// Chosen, not measured. The first is long enough that a beacon polling every
// few seconds asks the hub a few times an hour and not a few times a minute,
// and short enough that a pillar change reaches a beacon well inside the
// time it takes an operator to notice it has not. The second is so that a
// pillar that does not compile is retried, which a beacon that needs it
// would want, and not at the rate of the poll. DIVERGENCE 5.552.
const (
	beaconPillarTTL       = 5 * time.Minute
	beaconPillarFailedTTL = time.Minute
)

// pillarCache holds the pillar a beacon poll reads, for beaconPillarTTL.
//
// Every poll of every due beacon used to compile this node's pillar afresh,
// which on a node with a hub is a request to the hub, which compiles it,
// external sources included. At the default minute that is one compilation
// per beacon per node per minute; a file beacon set to seconds is one per
// few seconds. The grains and the pillar a beacon reads do not move at that
// rate, and the comment on the context said why it was fresh each time (a
// node polling against what the process started with reads a node that no
// longer exists), which a few minutes of staleness does not undo.
type pillarCache struct {
	load func() (*value.Map, error)
	now  func() time.Time
	warn func(msg string, kv ...any)

	mu      sync.Mutex
	pillar  *value.Map
	expires time.Time
}

func newPillarCache(load func() (*value.Map, error), warn func(msg string, kv ...any)) *pillarCache {
	return &pillarCache{load: load, now: time.Now, warn: warn}
}

// get answers with the cached pillar, or compiles it when the cache has
// expired. A pillar that did not compile is an empty one, as it was, and is
// cached for the shorter time, with one warning for each attempt.
func (c *pillarCache) get() *value.Map {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pillar != nil && c.now().Before(c.expires) {
		return c.pillar
	}
	p, err := c.load()
	if err != nil {
		if c.warn != nil {
			c.warn("compiling pillar for a beacon poll", "error", err.Error())
		}
		c.pillar = value.NewMap(0)
		c.expires = c.now().Add(beaconPillarFailedTTL)
		return c.pillar
	}
	c.pillar = p
	c.expires = c.now().Add(beaconPillarTTL)
	return p
}
