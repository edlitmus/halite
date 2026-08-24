// Package beacon is the input side of the automation loop: SPEC section
// 16's watchers, which look at the node and fire an event when
// something changes.
//
// A beacon here is a function over the node's own execution modules
// rather than a separate reader of the system. `diskusage` asks
// `disk.usage`, `load` asks `status.loadavg`. That keeps a beacon
// portable for free -- it works on whatever platform its module works
// on -- and it means a beacon and the state that acts on the same fact
// cannot disagree about what the fact is.
package beacon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// TagPrefix marks an event as a beacon's, so that whatever carries it
// can place it under `halite/beacon/<node_id>/`, the tag SPEC 17.1
// names. The node id is added by the hub from the certificate, never by
// the node, so this marks the kind of event and not who sent it.
const TagPrefix = "beacon/"

// Event is one beacon firing.
type Event struct {
	// Suffix is appended to `halite/beacon/<node>/<beacon>/`, so a
	// beacon names only the part that varies. Empty is allowed: the
	// beacon's own name is then the whole tag.
	Suffix string
	Data   map[string]any
}

// Func is one beacon. It is given the node's execution context and its
// own configured instance, and returns what it wants to say.
//
// Returning nothing is the normal case: a beacon that has seen no
// change says nothing, and the engine is what decides whether saying
// the same thing twice is worth an event.
type Func func(c *exec.Context, in *Instance) ([]Event, error)

// Module is a beacon together with what it is.
type Module struct {
	Name string
	Doc  string
	// Platforms restricts the beacon; empty means every platform.
	Platforms []string
	Fn        Func
	// Pending says when a declared-but-unbuilt beacon arrives, and is
	// the whole message an operator gets. SPEC 16.2 is an inventory,
	// and a name missing from the registry is indistinguishable from a
	// typo.
	Pending string
}

// Registry holds the beacons a build ships.
type Registry struct {
	mods map[string]Module
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{mods: map[string]Module{}} }

// Add registers beacons. Registering a name twice panics: it can only
// happen while a build wires itself up, and the alternative is a beacon
// serving under a name whose documentation belongs to another.
func (r *Registry) Add(mods ...Module) {
	for _, m := range mods {
		if _, dup := r.mods[m.Name]; dup {
			panic("beacon: " + m.Name + " is registered twice")
		}
		if m.Fn == nil && m.Pending == "" {
			panic("beacon: " + m.Name + " has no implementation and names no phase")
		}
		r.mods[m.Name] = m
	}
}

// Lookup finds a beacon by name.
func (r *Registry) Lookup(name string) (Module, bool) {
	m, ok := r.mods[name]
	return m, ok
}

// Has reports whether a name is registered, built or pending.
func (r *Registry) Has(name string) bool { _, ok := r.mods[name]; return ok }

// Names lists every beacon, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.mods))
	for n := range r.mods {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Instance is one configured beacon.
type Instance struct {
	// Name is the beacon it runs.
	Name string
	// Args is everything in the configuration that is not a control,
	// merged into one mapping. Salt's list-of-single-key-mappings form
	// is what produces it.
	Args *value.Map

	// Interval is how often the beacon is polled.
	Interval time.Duration
	// Delay is how long a condition must hold before it fires, which is
	// what stops a service that restarts in two seconds from being
	// reported as down.
	Delay time.Duration
	// OnChangeOnly suppresses an event identical to the last one.
	OnChangeOnly bool
	// EmitAtStartup fires on the first poll even with nothing changed.
	EmitAtStartup bool
	// DisableDuringStateRun suppresses the beacon while a state run is
	// in progress, which is what prevents a state run from triggering
	// itself. SPEC 16.3.
	DisableDuringStateRun bool
	// Disabled is `beacons.disable` at runtime.
	Disabled bool

	// RateLimit is the token bucket in events per second, default 10.
	RateLimit float64
	// CoalesceWindow collapses identical events, default one second.
	CoalesceWindow time.Duration
	// QueueDepth bounds this beacon's queue, default 1000.
	QueueDepth int

	// last is the digest of what each tag last said, for OnChangeOnly.
	last map[string]string
	// holding is when each tag's condition was first seen, for Delay.
	holding map[string]time.Time
	// started records that the first poll has happened.
	started bool
	// saidEnabled records that the configuration stated the enabled
	// state, so that modifying a beacon which says nothing about it
	// leaves it as it was.
	saidEnabled bool

	// bucketMu, tokens, and filled are the rate limiter.
	bucketMu sync.Mutex
	tokens   float64
	filled   time.Time

	// polledAt is when this beacon last ran, and polling marks it in
	// flight so a slow beacon delays its own next turn and nothing
	// else.
	polledAt time.Time
	polling  bool
}

// due reports whether this beacon's interval has elapsed.
func (in *Instance) due(now time.Time) bool {
	in.bucketMu.Lock()
	defer in.bucketMu.Unlock()
	if in.polledAt.IsZero() {
		return true
	}
	return now.Sub(in.polledAt) >= in.interval()
}

// claim marks the beacon in flight, or reports that it already is.
func (in *Instance) claim() bool {
	in.bucketMu.Lock()
	defer in.bucketMu.Unlock()
	if in.polling {
		return false
	}
	in.polling = true
	return true
}

func (in *Instance) release() {
	in.bucketMu.Lock()
	defer in.bucketMu.Unlock()
	in.polling = false
}

// markPolled records the time a poll started.
func (in *Instance) markPolled(now time.Time) {
	in.bucketMu.Lock()
	defer in.bucketMu.Unlock()
	in.polledAt = now
}

// The defaults of SPEC 16.3.
const (
	DefaultInterval       = time.Minute
	DefaultRateLimit      = 10
	DefaultCoalesceWindow = time.Second
	DefaultQueueDepth     = 1000
)

func (in *Instance) interval() time.Duration {
	if in.Interval > 0 {
		return in.Interval
	}
	return DefaultInterval
}

func (in *Instance) rateLimit() float64 {
	if in.RateLimit > 0 {
		return in.RateLimit
	}
	return DefaultRateLimit
}

func (in *Instance) coalesceWindow() time.Duration {
	if in.CoalesceWindow > 0 {
		return in.CoalesceWindow
	}
	return DefaultCoalesceWindow
}

func (in *Instance) queueDepth() int {
	if in.QueueDepth > 0 {
		return in.QueueDepth
	}
	return DefaultQueueDepth
}

// Arg reads one of the beacon's own settings.
func (in *Instance) Arg(name string) (any, bool) {
	if in.Args == nil {
		return nil, false
	}
	return in.Args.Get(name)
}

// StringArg reads a setting as a string.
func (in *Instance) StringArg(name, def string) string {
	v, ok := in.Arg(name)
	if !ok || v == nil {
		return def
	}
	return value.KeyString(v)
}

// BoolArg reads a setting as a boolean.
func (in *Instance) BoolArg(name string, def bool) bool {
	v, ok := in.Arg(name)
	if !ok {
		return def
	}
	return value.Truthy(v)
}

// controlNames are the settings that configure the engine rather than
// the beacon. Everything else is the beacon's own.
var controlNames = map[string]bool{
	"interval": true, "delay": true, "onchangeonly": true,
	"emitatstartup": true, "disable_during_state_run": true,
	"disabled": true, "enabled": true,
	"rate_limit": true, "coalesce_window": true, "queue_depth": true,
}

// IsControl reports whether a setting configures the engine.
func IsControl(name string) bool { return controlNames[name] }

// Parse reads the `beacons` configuration of SPEC 16.1.
//
// The schema is Salt's, including the list-of-single-key-mappings form:
// a beacon name maps to a list, each item is a mapping, and the items
// are merged into one configuration with the control settings taken
// out. An existing beacon configuration is written that way and has to
// keep working.
func Parse(v any) ([]*Instance, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("`beacons` is a mapping of beacon name to its configuration")
	}

	var out []*Instance
	for _, e := range m.Entries() {
		name := value.KeyString(e.Key)
		in, err := parseInstance(name, e.Val)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, nil
}

func parseInstance(name string, v any) (*Instance, error) {
	in := &Instance{
		Name:    name,
		Args:    value.NewMap(0),
		last:    map[string]string{},
		holding: map[string]time.Time{},
	}

	var items []any
	switch t := v.(type) {
	case nil:
		return in, nil
	case []any:
		items = t
	case *value.Map:
		// A mapping rather than a list is accepted, because it is what
		// an operator writes first and refusing it teaches nothing.
		items = []any{t}
	default:
		return nil, fmt.Errorf("beacon %s: the configuration is a list of mappings, not %s",
			name, value.TypeName(v))
	}

	for _, item := range items {
		entry, ok := item.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("beacon %s: each configuration item is a mapping, not %s",
				name, value.TypeName(item))
		}
		for _, e := range entry.Entries() {
			key := value.KeyString(e.Key)
			if !IsControl(key) {
				in.Args.Set(key, e.Val)
				continue
			}
			if err := applyControl(in, key, e.Val); err != nil {
				return nil, fmt.Errorf("beacon %s: %s: %w", name, key, err)
			}
		}
	}
	return in, nil
}

func applyControl(in *Instance, key string, v any) error {
	switch key {
	case "interval":
		d, err := asDuration(v)
		if err != nil {
			return err
		}
		in.Interval = d
	case "delay":
		d, err := asDuration(v)
		if err != nil {
			return err
		}
		in.Delay = d
	case "coalesce_window":
		d, err := asDuration(v)
		if err != nil {
			return err
		}
		in.CoalesceWindow = d
	case "onchangeonly":
		in.OnChangeOnly = value.Truthy(v)
	case "emitatstartup":
		in.EmitAtStartup = value.Truthy(v)
	case "disable_during_state_run":
		in.DisableDuringStateRun = value.Truthy(v)
	case "disabled":
		in.Disabled = value.Truthy(v)
		in.saidEnabled = true
	case "enabled":
		in.Disabled = !value.Truthy(v)
		in.saidEnabled = true
	case "rate_limit":
		n, err := asFloat(v)
		if err != nil {
			return err
		}
		in.RateLimit = n
	case "queue_depth":
		n, err := asFloat(v)
		if err != nil {
			return err
		}
		in.QueueDepth = int(n)
	}
	return nil
}

// asDuration reads a Go duration string or a bare number of seconds,
// which is what Salt's `interval: 5` means.
func asDuration(v any) (time.Duration, error) {
	switch t := v.(type) {
	case int64:
		return time.Duration(t) * time.Second, nil
	case float64:
		return time.Duration(t * float64(time.Second)), nil
	case string:
		if d, err := time.ParseDuration(t); err == nil {
			return d, nil
		}
		return 0, fmt.Errorf("%q is not a duration", t)
	}
	return 0, fmt.Errorf("%s is not a duration", value.TypeName(v))
}

func asFloat(v any) (float64, error) {
	switch t := v.(type) {
	case int64:
		return float64(t), nil
	case float64:
		return t, nil
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%g", &f); err == nil {
			return f, nil
		}
	}
	return 0, fmt.Errorf("%s is not a number", value.TypeName(v))
}

// digest is what "the same event as last time" means: the tag and the
// payload, rendered stably.
func digest(e Event) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", e.Suffix)
	for _, k := range sortedKeys(e.Data) {
		fmt.Fprintf(h, "%s\x00%v\x00", k, e.Data[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runtimeGOOS is a variable so a test can pretend to be elsewhere.
var runtimeGOOS = func() string { return goos }
