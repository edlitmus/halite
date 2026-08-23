package main

import (
	"context"

	"github.com/edlitmus/halite/internal/beacon"
	"github.com/edlitmus/halite/internal/cli"
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
	raw, ok := n.cfg.Get("beacons")
	if !ok || raw == nil {
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
		Context: func() *exec.Context {
			// A fresh context each poll: grains and pillar move, and a
			// beacon polling for a week against the set this process
			// started with is reading a node that no longer exists.
			return n.context(n.compilePillarOrNothing())
		},
		Send:         n.events.Send,
		StateRunning: n.stateRunning,
		Now:          nil,
		Log: func(level, msg string, kv ...any) {
			lv, _ := hlog.ParseLevel(level)
			n.log.Log(lv, msg, append([]any{"component", "beacon"}, kv...)...)
		},
	}
	names := make([]string, 0, len(instances))
	for _, in := range instances {
		names = append(names, in.Name)
	}
	n.log.Info("beacons started", "beacons", names)
	go func() {
		if err := engine.Run(ctx); err != nil {
			n.log.Error("the beacons stopped", "error", err.Error())
		}
	}()
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

// stateRunning reports whether a state run is in progress, which is
// what `disable_during_state_run` suppresses a beacon for: it is how a
// state run is stopped from triggering itself. SPEC 16.3.
func (n *node) stateRunning() bool { return n.statesRunning.Load() > 0 }

// enterStateRun and leaveStateRun bracket a state run.
func (n *node) enterStateRun() { n.statesRunning.Add(1) }
func (n *node) leaveStateRun() { n.statesRunning.Add(-1) }
