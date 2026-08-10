// Package beacon watches things on a managed host and raises events when
// they change: a disk filling up, a service falling over, a file being
// edited underneath the configuration.
//
// Beacons are edge triggered. A condition that stays true raises one event
// when it becomes true and one when it clears, never one per check — a
// disk sitting at 95% must not fill the bus with an event a minute, and a
// reactor rule keyed on it must not fire a job a minute.
package beacon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// DefaultInterval is how often a beacon checks when its config does not
// say. Slow enough to be free, fast enough to notice.
const DefaultInterval = 60 * time.Second

// Emission is one event a beacon wants raised.
type Emission struct {
	// Data goes into the event body. The beacon's name and the agent's id
	// make up the tag.
	Data map[string]any
}

// Beacon watches one thing on the host.
type Beacon interface {
	// Name is the tag segment: halite/beacon/<agent>/<name>.
	Name() string
	// Interval is how often Check runs.
	Interval() time.Duration
	// Check runs once and returns the events to raise, if any. It is called
	// from a single goroutine per beacon, so implementations may keep
	// unsynchronised state to compare against.
	Check() []Emission
}

// Emitter raises an event. The agent supplies one that posts to the
// control plane.
type Emitter func(name string, data map[string]any)

// Runner ticks every configured beacon on its own schedule.
type Runner struct {
	beacons []Beacon
	emit    Emitter
	log     *log.Logger
}

// NewRunner builds a runner. It does nothing until Run is called.
func NewRunner(beacons []Beacon, emit Emitter, logger *log.Logger) *Runner {
	return &Runner{beacons: beacons, emit: emit, log: logger}
}

// Count reports how many beacons are configured, for startup logging.
func (r *Runner) Count() int { return len(r.beacons) }

// Run ticks each beacon until ctx is cancelled, then waits for the ticks
// in flight to finish.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, b := range r.beacons {
		wg.Add(1)
		go func(b Beacon) {
			defer wg.Done()
			r.watch(ctx, b)
		}(b)
	}
	wg.Wait()
}

func (r *Runner) watch(ctx context.Context, b Beacon) {
	interval := b.Interval()
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// The first check runs immediately: a service that is already down when
	// the agent starts should be reported now, not one interval from now.
	r.tick(b)
	for {
		select {
		case <-ticker.C:
			r.tick(b)
		case <-ctx.Done():
			return
		}
	}
}

// tick runs one check, guarding against a beacon that panics: a broken
// watcher must not take the agent down with it.
func (r *Runner) tick(b Beacon) {
	defer func() {
		if problem := recover(); problem != nil {
			r.log.Printf("beacon %s: panicked: %v", b.Name(), problem)
		}
	}()
	for _, emission := range b.Check() {
		r.emit(b.Name(), emission.Data)
	}
}

// state tracks an edge-triggered condition. Beacons embed it rather than
// each reinventing "only tell me when this changes".
type state struct {
	alerting bool
	started  bool
}

// transition reports whether the condition changed, and records the new
// value. The first call establishes the baseline: it reports a change only
// if the condition is already true, so a host that boots with a full disk
// says so immediately while a healthy one stays quiet.
func (s *state) transition(alerting bool) (changed bool) {
	if !s.started {
		s.started = true
		s.alerting = alerting
		return alerting
	}
	if s.alerting == alerting {
		return false
	}
	s.alerting = alerting
	return true
}

// parseInterval reads a duration from a config value.
func parseInterval(raw string) (time.Duration, error) {
	if raw == "" {
		return DefaultInterval, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("interval %q: %w", raw, err)
	}
	if interval < time.Second {
		return 0, fmt.Errorf("interval %q is too short (one second minimum)", raw)
	}
	return interval, nil
}
