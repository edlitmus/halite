package builtin

import (
	"fmt"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// The `beacon` and `schedule` state modules of SPEC section 15.5.
//
// Both execution modules ship whole — ten functions and twelve — and
// neither had a state, so a tree could manage a beacon or a scheduled
// job from the command line and could not *declare* one. That is the
// wrong way round for a configuration management system: the whole point
// is that the file says what the node should be.
//
// # Why these two are together
//
// They are the same state twice. A beacon and a scheduled job are both a
// named entry in a mapping the node holds, both are changed through a
// control interface on the context, both are persisted by writing the
// running set back to a file, and both are declared by naming one and
// giving it a configuration whose keys belong to the thing being
// configured rather than to the state. Writing them separately would
// have meant writing the same four decisions twice and getting one of
// them different.
//
// # The configuration is an open set of keys
//
// A beacon's configuration is the beacon's own — `inotify` takes
// `files`, `diskusage` takes mount points — and a scheduled job's is
// the scheduler's. So both states take `AnyKwargs`, the same way
// `beacons.add` does, and a tree migrating from Salt writes what it
// already writes:
//
//	inotify:
//	  beacon.present:
//	    - files:
//	        /etc/nginx/nginx.conf:
//	          mask: [modify]
//	    - interval: 5
//
// What refuses a key the beacon does not know is the beacon, not this.

// watcher is the difference between the two states, so that everything
// else can be written once.
type watcher struct {
	// module is `beacon` or `schedule`.
	module string
	// thing is what one of them is called in a sentence.
	thing string
	// dataArg is the explicit spelling of the configuration argument,
	// matching the execution module's: `beacon_data` or `job`.
	dataArg string
	// kind is what SaveConfig calls this set: `beacons` or `schedule`.
	kind string
	// section is where SPEC describes it.
	section string
	// control returns the interface, or nil when this node is not
	// running them.
	control func(*exec.Context) watcherControl
}

// watcherControl is the part of BeaconControl and ScheduleControl these
// states use. Both satisfy it already; naming the subset is what lets
// one implementation drive both.
type watcherControl interface {
	Add(name string, config *value.Map) error
	Modify(name string, config *value.Map) error
	Delete(name string) error
	Snapshot() *value.Map
}

var beaconWatcher = watcher{
	module: "beacon", thing: "beacon", dataArg: "beacon_data",
	kind: "beacons", section: "16.1",
	control: func(c *exec.Context) watcherControl {
		if c.Beacons == nil {
			return nil
		}
		return c.Beacons
	},
}

var scheduleWatcher = watcher{
	module: "schedule", thing: "scheduled job", dataArg: "job",
	kind: "schedule", section: "20.1",
	control: func(c *exec.Context) watcherControl {
		if c.Schedule == nil {
			return nil
		}
		return c.Schedule
	},
}

func registerWatcherStates(r *Registries) {
	for _, w := range []watcher{beaconWatcher, scheduleWatcher} {
		w := w
		r.States.Add(
			states.Module{
				Sig: signature.Signature{
					Module: w.module, Function: "present",
					Doc: fmt.Sprintf(
						"Ensure this node is running a %s with the configuration declared.", w.thing),
					Params: []signature.Param{
						nameParam(fmt.Sprintf("The %s. Defaults to the state ID.", w.thing)),
						opt(w.dataArg, signature.Map, nil,
							"The configuration. Its keys may also be written directly on the state."),
						opt("save", signature.Bool, true,
							"Write the running set to disk so it survives a restart."),
					},
					Mutates:  true,
					TestMode: signature.TestReliable,
					Section:  "15.5",
					// The configuration is an open set of keys belonging
					// to the thing being configured, so this state does
					// not know them and must not refuse them.
					AnyKwargs: true,
				},
				Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
					return w.present(c, args)
				},
			},
			states.Module{
				Sig: signature.Signature{
					Module: w.module, Function: "absent",
					Doc: fmt.Sprintf("Ensure this node is not running a %s by this name.", w.thing),
					Params: []signature.Param{
						nameParam(fmt.Sprintf("The %s. Defaults to the state ID.", w.thing)),
						opt("save", signature.Bool, true,
							"Write the running set to disk so the removal survives a restart."),
					},
					Mutates:  true,
					TestMode: signature.TestReliable,
					Section:  "15.5",
				},
				Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
					return w.absent(c, args)
				},
			},
		)
	}
}

// notRunning is what a node that is not running these is told.
//
// A failure rather than a success that changed nothing. A state
// declaring a beacon on a node with no beacon engine has not been
// satisfied, and reporting that it had would mean a highstate passing
// over a node that is not watching anything.
func (w watcher) notRunning() string {
	return fmt.Sprintf(
		"this node is not running %s, so there is nothing to declare; "+
			"`%s` in the node configuration is what starts them", w.kind, w.kind)
}

// declared reads the configuration out of the state's arguments.
//
// Either spelling: an explicit `beacon_data`/`job` mapping, or the keys
// written directly on the state, which is what a Salt tree does. The
// state's own arguments are removed from the second form — a beacon
// whose configuration carried `save: true` because the state took one
// would be a beacon configured with a key the state meant for itself.
func (w watcher) declared(args *value.Map) *value.Map {
	if raw, ok := args.Get(w.dataArg); ok && raw != nil {
		if m, isMap := raw.(*value.Map); isMap {
			return m
		}
		// A list, which is the form a configuration file uses and the
		// one an operator writing YAML reaches for first.
		out := value.NewMap(1)
		out.Set(w.dataArg, raw)
		return out
	}
	out := value.NewMap(args.Len())
	for _, e := range args.Entries() {
		switch key := value.KeyString(e.Key); key {
		case "name", "save", w.dataArg:
		default:
			if e.Val != nil {
				out.Set(key, e.Val)
			}
		}
	}
	return out
}

// current returns what this node holds for a name, or nil.
//
// From Snapshot rather than List: a snapshot is the configuration in the
// shape a file uses, and a listing carries what the engine knows at this
// instant — a job's next fire time, a beacon's last event. Comparing a
// declaration against the second would report a change every time the
// clock moved.
func (w watcher) current(ctl watcherControl, name string) *value.Map {
	v, ok := ctl.Snapshot().Get(name)
	if !ok || v == nil {
		return nil
	}
	m, _ := v.(*value.Map)
	return m
}

func (w watcher) present(c *exec.Context, args *value.Map) (states.Result, error) {
	ctl := w.control(c)
	if ctl == nil {
		return states.False(w.notRunning()), nil
	}
	name := states.Str(args, "name", "")
	if name == "" {
		return states.False(fmt.Sprintf("%s.present needs a name.", w.module)), nil
	}
	want := w.declared(args)
	if want.Len() == 0 {
		return states.False(fmt.Sprintf(
			"%s.present needs a configuration for %s, either as `%s` or as keys on the state.",
			w.module, name, w.dataArg)), nil
	}

	have := w.current(ctl, name)
	if have != nil && sameWatcherConfig(want, have) {
		return states.True(fmt.Sprintf("%s is already configured as declared.", name)), nil
	}

	changes := value.NewMap(1)
	changes.Set(name, states.Change(watcherWas(have), "as declared"))
	if c.Test {
		verb := "added"
		if have != nil {
			verb = "changed"
		}
		return states.WouldChange(
			fmt.Sprintf("the %s %s would be %s.", w.thing, name, verb), changes), nil
	}

	var err error
	verb := "added"
	if have != nil {
		verb = "changed"
		err = ctl.Modify(name, want)
	} else {
		err = ctl.Add(name, want)
	}
	if err != nil {
		return states.False(fmt.Sprintf("the %s %s could not be %s: %v",
			w.thing, name, verb, err)), nil
	}

	res := states.Changed(fmt.Sprintf("the %s %s was %s.", w.thing, name, verb), changes)
	w.persist(c, ctl, args, &res)
	return res, nil
}

func (w watcher) absent(c *exec.Context, args *value.Map) (states.Result, error) {
	ctl := w.control(c)
	if ctl == nil {
		return states.False(w.notRunning()), nil
	}
	name := states.Str(args, "name", "")
	if name == "" {
		return states.False(fmt.Sprintf("%s.absent needs a name.", w.module)), nil
	}

	if w.current(ctl, name) == nil {
		return states.True(fmt.Sprintf("%s is not configured.", name)), nil
	}

	changes := value.NewMap(1)
	changes.Set(name, states.Change("configured", "absent"))
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("the %s %s would be removed.", w.thing, name), changes), nil
	}
	if err := ctl.Delete(name); err != nil {
		return states.False(fmt.Sprintf("the %s %s could not be removed: %v",
			w.thing, name, err)), nil
	}

	res := states.Changed(fmt.Sprintf("the %s %s was removed.", w.thing, name), changes)
	w.persist(c, ctl, args, &res)
	return res, nil
}

// persist writes the running set to disk.
//
// On by default, because a declaration that a node forgets on the next
// restart is not a declaration — the state would report a change, the
// node would reboot, and the next highstate would report the same change
// again. A tree that wants a beacon only for the life of this boot says
// `save: False`.
//
// A failure to write is a warning rather than a failure: the beacon is
// running, and saying the state failed would be reporting the wrong
// thing. The warning says what was lost.
func (w watcher) persist(c *exec.Context, ctl watcherControl, args *value.Map, res *states.Result) {
	if !states.Bool(args, "save", true) {
		return
	}
	if c.SaveConfig == nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"this node has nowhere to write its %s, so this change is lost on restart", w.kind))
		return
	}
	if _, err := c.SaveConfig(w.kind, ctl.Snapshot()); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"the %s were changed but could not be written, so this change is lost on restart: %v",
			w.kind, err))
	}
}

func watcherWas(have *value.Map) string {
	if have == nil {
		return "absent"
	}
	return "configured differently"
}

// sameWatcherConfig compares a declaration against what the node holds.
//
// Through the canonical YAML encoding, which is what `grains.present`
// does and for the same reason: these are arbitrary nested documents,
// the ordered map has no equality of its own, and an encoder that is
// stable for a given document is exactly the comparison wanted.
//
// Only the keys the declaration stated. A snapshot carries the defaults
// the engine filled in — a beacon's `interval`, a job's `maxrunning` —
// and comparing those against a declaration that never mentioned them
// would report a change on every run.
func sameWatcherConfig(declared, current *value.Map) bool {
	for _, e := range declared.Entries() {
		key := value.KeyString(e.Key)
		got, ok := current.Get(key)
		if !ok {
			return false
		}
		if encodeWatcherValue(e.Val) != encodeWatcherValue(got) {
			return false
		}
	}
	return true
}

func encodeWatcherValue(v any) string {
	return yaml.Encode(value.MapOf("v", v), yaml.EncodeOptions{Indent: 2})
}
