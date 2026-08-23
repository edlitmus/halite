package beacon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/exec"
)

// Engine polls the configured beacons and forwards what they say.
//
// Beacon events are the classic source of a self-inflicted denial of
// service: a file that changes in a loop fires a beacon that fires a
// reactor that changes the file. SPEC 16.3 addresses it at the source,
// and this is where: a token bucket per instance, coalescing of
// identical events, a bounded queue that drops the oldest and reports
// the count, and suppression while a state run is in progress.
type Engine struct {
	Registry *Registry
	// Instances is what the configuration asked for.
	Instances []*Instance
	// Context builds the execution context each poll runs under. A
	// function rather than a value, because grains and pillar move and
	// a beacon polling for an hour on a stale context is reading a
	// stale node.
	Context func() *exec.Context
	// Send forwards one event. The node's implementation puts it on the
	// hub's bus, which namespaces it under this node.
	Send func(tag string, data map[string]any) error
	// StateRunning reports whether a state run is in progress, for
	// `disable_during_state_run`.
	StateRunning func() bool
	// Log receives a line. Nil discards.
	Log func(level, msg string, kv ...any)
	// Now is the clock, for the tests.
	Now func() time.Time

	queue *coalescingQueue
	mu    sync.Mutex
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) logf(level, msg string, kv ...any) {
	if e.Log != nil {
		e.Log(level, msg, kv...)
	}
}

func (e *Engine) stateRunning() bool {
	return e.StateRunning != nil && e.StateRunning()
}

// Run polls until the context ends.
func (e *Engine) Run(ctx context.Context) error {
	if e.Registry == nil || e.Send == nil || e.Context == nil {
		return fmt.Errorf("a beacon engine needs a registry, a context, and somewhere to send")
	}
	live, err := e.check()
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return nil
	}

	e.queue = newCoalescingQueue(totalDepth(live))
	go e.queue.wake(ctx, 50*time.Millisecond)
	go e.forward(ctx)

	var wg sync.WaitGroup
	for _, in := range live {
		wg.Add(1)
		go func(in *Instance) {
			defer wg.Done()
			e.poll(ctx, in)
		}(in)
	}
	wg.Wait()
	return nil
}

// check refuses a configuration that names a beacon this build does not
// have, and reports the ones that are declared and not built.
//
// Refusing rather than skipping: a beacon that is configured and never
// runs is indistinguishable from a quiet estate, which is the worst
// available outcome for a watcher.
func (e *Engine) check() ([]*Instance, error) {
	var live []*Instance
	for _, in := range e.Instances {
		mod, ok := e.Registry.Lookup(in.Name)
		if !ok {
			return nil, fmt.Errorf("%s is not a beacon this build has; `beacons.list --available` says which are", in.Name)
		}
		if mod.Pending != "" {
			return nil, fmt.Errorf("the %s beacon is declared and not built yet: it arrives in %s",
				in.Name, mod.Pending)
		}
		if err := checkPlatform(mod); err != nil {
			return nil, err
		}
		if in.Disabled {
			e.logf("info", "beacon disabled by configuration", "beacon", in.Name)
			continue
		}
		live = append(live, in)
	}
	return live, nil
}

func totalDepth(live []*Instance) int {
	total := 0
	for _, in := range live {
		total += in.queueDepth()
	}
	return total
}

// poll runs one beacon on its interval.
func (e *Engine) poll(ctx context.Context, in *Instance) {
	ticker := time.NewTicker(in.interval())
	defer ticker.Stop()
	for {
		e.once(in)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// once is one poll of one beacon, with the SPEC 16.3 controls applied.
func (e *Engine) once(in *Instance) {
	if in.DisableDuringStateRun && e.stateRunning() {
		return
	}
	mod, ok := e.Registry.Lookup(in.Name)
	if !ok {
		return
	}

	events, err := mod.Fn(e.Context(), in)
	if err != nil {
		e.logf("warn", "a beacon failed", "beacon", in.Name, "error", err.Error())
		// A beacon that cannot read the thing it watches is itself
		// worth an event: silence is what a healthy system looks like,
		// and a watcher that has stopped watching must not look like
		// one.
		e.offer(in, Event{Suffix: "error", Data: map[string]any{"error": err.Error()}})
		return
	}

	first := !in.started
	in.started = true
	now := e.now()
	for _, ev := range events {
		if !e.held(in, ev, now) {
			continue
		}
		if in.OnChangeOnly && !e.changed(in, ev) && !(first && in.EmitAtStartup) {
			continue
		}
		e.remember(in, ev)
		e.offer(in, ev)
	}
	e.forget(in, events, now)
}

// held applies `delay`: a condition has to hold for that long before it
// is worth saying, which is what stops a service that restarts in two
// seconds from being reported as down.
func (e *Engine) held(in *Instance, ev Event, now time.Time) bool {
	if in.Delay <= 0 {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	since, seen := in.holding[ev.Suffix]
	if !seen {
		in.holding[ev.Suffix] = now
		return false
	}
	return now.Sub(since) >= in.Delay
}

// forget drops the delay state for conditions that have gone away, so
// that a condition returning later starts its delay again.
func (e *Engine) forget(in *Instance, events []Event, now time.Time) {
	if in.Delay <= 0 {
		return
	}
	present := make(map[string]bool, len(events))
	for _, ev := range events {
		present[ev.Suffix] = true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for suffix := range in.holding {
		if !present[suffix] {
			delete(in.holding, suffix)
		}
	}
}

func (e *Engine) changed(in *Instance, ev Event) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return in.last[ev.Suffix] != digest(ev)
}

func (e *Engine) remember(in *Instance, ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	in.last[ev.Suffix] = digest(ev)
}

// offer applies the rate limit and puts the event on the queue.
func (e *Engine) offer(in *Instance, ev Event) {
	if !in.allow(e.now()) {
		e.logf("debug", "a beacon event was rate limited", "beacon", in.Name)
		return
	}
	tag := in.Name
	if ev.Suffix != "" {
		tag = in.Name + "/" + ev.Suffix
	}
	dropped := e.queue.push(queued{
		tag:    tag,
		data:   ev.Data,
		key:    in.Name + "\x00" + digest(ev),
		at:     e.now(),
		window: in.coalesceWindow(),
	})
	if dropped > 0 {
		e.logf("warn", "the beacon queue overflowed", "beacon", in.Name, "dropped", dropped)
		// Loss is reported, never silent. SPEC 16.3.
		_ = e.Send(in.Name+"/overflow", map[string]any{"dropped": int64(dropped)})
	}
}

// allow is the per-instance token bucket.
func (in *Instance) allow(now time.Time) bool {
	in.bucketMu.Lock()
	defer in.bucketMu.Unlock()
	limit := in.rateLimit()
	if in.filled.IsZero() {
		in.filled, in.tokens = now, limit
	}
	in.tokens += now.Sub(in.filled).Seconds() * limit
	if in.tokens > limit {
		in.tokens = limit
	}
	in.filled = now
	if in.tokens < 1 {
		return false
	}
	in.tokens--
	return true
}

// forward sends what the queue holds, once each event's coalescing
// window has closed.
func (e *Engine) forward(ctx context.Context) {
	for {
		item, ok := e.queue.pop(ctx, e.now)
		if !ok {
			return
		}
		data := item.data
		if item.count > 1 {
			// The count is what makes coalescing honest: one event
			// stands for several, and says how many.
			if data == nil {
				data = map[string]any{}
			}
			data["_count"] = int64(item.count)
		}
		if err := e.Send(item.tag, data); err != nil {
			e.logf("warn", "a beacon event could not be sent",
				"tag", item.tag, "error", err.Error())
		}
	}
}

// checkPlatform refuses a beacon on a platform it cannot work on,
// rather than running it and reporting nothing.
func checkPlatform(mod Module) error {
	if len(mod.Platforms) == 0 {
		return nil
	}
	for _, p := range mod.Platforms {
		if p == runtimeGOOS() {
			return nil
		}
	}
	return fmt.Errorf("the %s beacon runs on %v, and this node is %s",
		mod.Name, mod.Platforms, runtimeGOOS())
}
