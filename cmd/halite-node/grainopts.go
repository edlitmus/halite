package main

import (
	"time"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/grains"
)

// grainOptions is how this node collects its grains.
//
// One function rather than the three copies this used to be: startup,
// the refresh ticker, and `saltutil.refresh_grains` each built the
// options themselves, so a new setting reached whichever of them the
// change happened to touch. `cloud_grains` was collected at startup and
// not on refresh for exactly that reason.
func grainOptions(cfg *config.Config, nodeID, root string) grains.Options {
	return grains.Options{
		NodeID:     nodeID,
		StaticFile: root + "/grains",
		GrainsDir:  root + "/grains.d",
		Extra:      cfg.Map("grains"),
		Cloud:      cfg.Bool("cloud_grains", false),
		CloudOptions: grains.CloudOptions{
			Timeout: cfg.Duration("cloud_grains_timeout", 10*time.Second),
			Exclude: cfg.StringSlice("cloud_grains_exclude"),
		},
	}
}
