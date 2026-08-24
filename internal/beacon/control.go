package beacon

import (
	"fmt"
	"sort"

	"github.com/edlitmus/halite/internal/value"
)

// The runtime management of SPEC section 16.1: `beacons.add`,
// `modify`, `delete`, `enable`, `disable`, `list`, `save`, and `reset`.
//
// A beacon configuration that can only be changed by restarting the
// node is one an operator changes rarely and carefully, which sounds
// like a virtue and is not: the case for changing a watcher is usually
// that it is firing during an incident, and the last thing an incident
// needs is a node restart.

// List describes the configured beacons, as `beacons.list` returns
// them.
func (e *Engine) List() *value.Map {
	e.mu.Lock()
	defer e.mu.Unlock()

	names := make([]string, 0, len(e.Instances))
	byName := make(map[string]*Instance, len(e.Instances))
	for _, in := range e.Instances {
		names = append(names, in.Name)
		byName[in.Name] = in
	}
	sort.Strings(names)

	out := value.NewMap(len(names))
	for _, name := range names {
		in := byName[name]
		entry := value.NewMap(7)
		entry.Set("interval", in.interval().String())
		if in.Delay > 0 {
			entry.Set("delay", in.Delay.String())
		}
		if in.OnChangeOnly {
			entry.Set("onchangeonly", true)
		}
		if in.DisableDuringStateRun {
			entry.Set("disable_during_state_run", true)
		}
		entry.Set("enabled", !in.Disabled && !e.paused)
		if in.Disabled {
			entry.Set("disabled", true)
		}
		entry.Set("config", in.Args)
		out.Set(name, entry)
	}
	return out
}

// Add installs a beacon on a running node.
//
// The configuration is checked against the registry before it is
// accepted, so a name this build does not have is refused at the point
// of asking rather than by going quiet afterwards.
func (e *Engine) Add(name string, config *value.Map) error {
	if name == "" {
		return fmt.Errorf("a beacon needs a name")
	}
	in, err := parseInstance(name, configItems(config))
	if err != nil {
		return err
	}
	if err := e.checkOne(in); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, existing := range e.Instances {
		if existing.Name == name {
			return fmt.Errorf("%s is already configured; `beacons.modify` changes one", name)
		}
	}
	e.Instances = append(e.Instances, in)
	e.logf("info", "beacon added", "beacon", name)
	return nil
}

// Modify replaces a beacon's configuration.
func (e *Engine) Modify(name string, config *value.Map) error {
	in, err := parseInstance(name, configItems(config))
	if err != nil {
		return err
	}
	if err := e.checkOne(in); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for i, existing := range e.Instances {
		if existing.Name != name {
			continue
		}
		// The disabled flag survives a modification unless the new
		// configuration names it: an operator fixing a threshold on a
		// beacon they turned off during an incident does not mean to
		// turn it back on.
		if !in.saidEnabled {
			in.Disabled = existing.Disabled
		}
		e.Instances[i] = in
		e.logf("info", "beacon modified", "beacon", name)
		return nil
	}
	return fmt.Errorf("%s is not configured on this node", name)
}

// Delete removes a beacon.
func (e *Engine) Delete(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, in := range e.Instances {
		if in.Name != name {
			continue
		}
		e.Instances = append(e.Instances[:i], e.Instances[i+1:]...)
		e.logf("info", "beacon deleted", "beacon", name)
		return nil
	}
	return fmt.Errorf("%s is not configured on this node", name)
}

// SetEnabled turns one beacon on or off, or every beacon when the name
// is empty.
//
// Disabling all of them holds them without forgetting them, which is
// what `beacons.disable` means and what makes it safe to use during an
// incident: `beacons.enable` afterwards restores exactly what was
// there.
func (e *Engine) SetEnabled(name string, on bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if name == "" {
		e.paused = !on
		e.logf("info", "beacons paused", "paused", e.paused)
		return nil
	}
	for _, in := range e.Instances {
		if in.Name != name {
			continue
		}
		in.Disabled = !on
		e.logf("info", "beacon enabled state changed", "beacon", name, "enabled", on)
		return nil
	}
	return fmt.Errorf("%s is not configured on this node", name)
}

// Reset drops every beacon.
func (e *Engine) Reset() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Instances = nil
	e.logf("warn", "every beacon was removed")
	return nil
}

// Snapshot renders the running configuration in the shape the
// configuration file uses, which is what `beacons.save` writes.
func (e *Engine) Snapshot() *value.Map {
	e.mu.Lock()
	defer e.mu.Unlock()

	names := make([]string, 0, len(e.Instances))
	byName := make(map[string]*Instance, len(e.Instances))
	for _, in := range e.Instances {
		names = append(names, in.Name)
		byName[in.Name] = in
	}
	sort.Strings(names)

	out := value.NewMap(len(names))
	for _, name := range names {
		in := byName[name]
		// The list-of-single-key-mappings form, because that is what
		// the loader reads and what an operator's existing files look
		// like. A file this writes has to be one a person can edit.
		items := make([]any, 0, in.Args.Len()+4)
		for _, arg := range in.Args.Entries() {
			items = append(items, value.MapOf(arg.Key, arg.Val))
		}
		items = append(items, value.MapOf("interval", int64(in.interval().Seconds())))
		if in.Delay > 0 {
			items = append(items, value.MapOf("delay", int64(in.Delay.Seconds())))
		}
		if in.OnChangeOnly {
			items = append(items, value.MapOf("onchangeonly", true))
		}
		if in.DisableDuringStateRun {
			items = append(items, value.MapOf("disable_during_state_run", true))
		}
		if in.Disabled {
			items = append(items, value.MapOf("disabled", true))
		}
		out.Set(name, items)
	}
	return out
}

// configItems accepts a beacon's configuration as a mapping or as the
// list form the file uses, so that `beacons.add` reads what an operator
// would have written in a file.
func configItems(config *value.Map) any {
	if config == nil {
		return nil
	}
	if raw, ok := config.Get("beacon_data"); ok {
		// Salt's spelling for "here is the configuration", which an
		// existing caller uses.
		return raw
	}
	return config
}
