package main

import (
	"context"
	"fmt"

	"path/filepath"

	"github.com/edlitmus/halite/internal/beacon"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/exec"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/value"
)

// startBeacons runs the watchers of SPEC section 16 for as long as the
// node is connected.
//
// Only when connected: a beacon fires an event, an event goes to the
// hub's bus, and a beacon on a node with nowhere to send is a poll loop
// that reads the disk and discards the answer.
func (n *node) startBeacons(ctx context.Context) {
	raw, err := n.beaconConfig()
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if raw == nil {
		return
	}
	instances, err := beacon.Parse(raw)
	if err != nil {
		// A beacon configuration that will not parse stops the node
		// rather than starting one whose watchers are silently absent.
		// A watcher that is configured and does not run is
		// indistinguishable from a quiet machine.
		cli.Fatalf("%v", err)
	}
	if len(instances) == 0 {
		return
	}
	if n.events == nil {
		n.log.Warn("beacons are configured and this node has no hub to send to; not starting them",
			"beacons", len(instances))
		return
	}

	engine := &beacon.Engine{
		Registry:  beacon.New(),
		Instances: instances,
		Tick:      n.cfg.Duration("beacon_tick", 0),
		Context: func() *exec.Context {
			// A fresh context each poll: grains and pillar move, and a
			// beacon polling for a week against the set this process
			// started with is reading a node that no longer exists.
			return n.context(n.compilePillarOrNothing())
		},
		// Through the node rather than the sender it holds now: the
		// client is rebuilt on every reconnect, and a captured method
		// value would keep firing at the one this node started with.
		Send:         n.sendEvent,
		StateRunning: n.stateRunning,
		Now:          nil,
		Log: func(level, msg string, kv ...any) {
			lv, _ := hlog.ParseLevel(level)
			n.log.Log(lv, msg, append([]any{"component", "beacon"}, kv...)...)
		},
		Observe: n.metrics.observeBeacon,
	}
	names := make([]string, 0, len(instances))
	for _, in := range instances {
		names = append(names, in.Name)
	}
	// Checked here rather than inside the loop, so a beacon this build
	// does not have stops the node at startup the way a configuration
	// that will not parse does. It used to be discovered by the
	// goroutine, which logged and left the node running with no
	// watchers at all.
	if err := engine.Check(); err != nil {
		cli.Fatalf("%v", err)
	}
	n.beacons = engine
	n.metrics.gauge("halite_beacon_queue_depth",
		"Beacon events waiting to be sent to the hub.",
		func() float64 { return float64(engine.Depth()) })
	n.log.Info("beacons started", "beacons", names)
	go func() {
		if err := engine.Run(ctx); err != nil {
			n.log.Error("the beacons stopped", "error", err.Error())
		}
	}()
}

// beaconConfig is `beacons` from the node configuration, merged with
// every fragment in `beacons.d`.
//
// SPEC 16.1 names three sources: the configuration file, that
// directory, and pillar. The directory is also where the node writes
// its own runtime changes, in a file of its own, so that `beacons.save`
// never has to edit what a package manager put there.
func (n *node) beaconConfig() (any, error) {
	base, _ := n.cfg.Get("beacons")
	dropIns, files, err := config.LoadDefinitions(n.beaconDir(), "beacons")
	if err != nil {
		return nil, err
	}
	if dropIns == nil {
		return base, nil
	}
	if len(files) > 0 {
		n.log.Info("beacon fragments read", "files", files)
	}
	if base == nil {
		return dropIns, nil
	}
	return value.Merge(base, dropIns, value.MergeOpts{}), nil
}

func (n *node) beaconDir() string {
	return filepath.Join(n.root, "beacons.d")
}

// compilePillarOrNothing gives a beacon the pillar if it is available
// and an empty one if it is not.
//
// A hub that cannot be reached must not stop the watchers: a beacon
// that reports a full disk is most useful exactly when something else
// is wrong.
func (n *node) compilePillarOrNothing() *value.Map {
	p, err := n.compilePillarOrErr()
	if err != nil {
		n.log.Warn("compiling pillar for a beacon poll", "error", err.Error())
		return value.NewMap(0)
	}
	return p
}

// sendEvent puts an event on the hub's bus through whichever sender
// this node is attached to now.
func (n *node) sendEvent(tag string, data map[string]any) error {
	if n.events == nil {
		return fmt.Errorf("this node has no hub to send an event to")
	}
	return n.events.Send(tag, data)
}

// stateRunning reports whether a state run is in progress, which is
// what `disable_during_state_run` suppresses a beacon for: it is how a
// state run is stopped from triggering itself. SPEC 16.3.
func (n *node) stateRunning() bool { return n.statesRunning.Load() > 0 }

// enterStateRun and leaveStateRun bracket a state run.
func (n *node) enterStateRun() { n.statesRunning.Add(1) }
func (n *node) leaveStateRun() { n.statesRunning.Add(-1) }
