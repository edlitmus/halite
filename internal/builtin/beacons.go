package builtin

import (
	"fmt"

	"github.com/edlitmus/halite/internal/beacon"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// registerBeacons installs the beacon management functions of SPEC
// section 16.1.
//
// `beacons.list` answers from the registry and the configuration, which
// needs nothing running. The eight that change a running node's
// watchers need a handle on the engine and somewhere to write the
// change down, and say which phase that is rather than reporting a
// change nobody made.
func registerBeacons(r *Registries) {
	sig := func(function, doc string, params ...signature.Param) signature.Signature {
		return signature.Signature{
			Module: "beacons", Function: function, Doc: doc,
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "16.1",
			Params:   params,
			// A beacon's configuration is an open set of keys -- each
			// beacon has its own -- and an operator types them inline:
			// `beacons.add name=diskusage /=85% interval=60`. The
			// parser is what refuses a key the beacon does not know.
			AnyKwargs: true,
		}
	}
	name := req("name", signature.String, "The beacon.")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "beacons", Function: "list",
				Doc: "The beacons this node is running, and what each is set to. " +
					"With `available`, the beacons this build ships instead.",
				TestMode: signature.TestNotApplicable,
				Section:  "16.1",
				Params: []signature.Param{
					opt("available", signature.Bool, false,
						"List what this build ships rather than what this node runs."),
				},
			},
			Fn: listBeacons,
		},
		exec.Module{
			Sig: sig("add", "Add a beacon to a running node.",
				name,
				opt("beacon_data", signature.Map, nil, "The beacon's configuration."),
			),
			Fn: withBeacons(func(c *exec.Context, args *value.Map, ctl exec.BeaconControl) (any, error) {
				return changed(c, ctl.Add(beaconName(args), beaconConfig(args)))
			}),
		},
		exec.Module{
			Sig: sig("modify", "Change a running node's beacon.",
				name,
				opt("beacon_data", signature.Map, nil, "The beacon's configuration."),
			),
			Fn: withBeacons(func(c *exec.Context, args *value.Map, ctl exec.BeaconControl) (any, error) {
				return changed(c, ctl.Modify(beaconName(args), beaconConfig(args)))
			}),
		},
		exec.Module{
			Sig: sig("delete", "Remove a beacon from a running node.", name),
			Fn: withBeacons(func(c *exec.Context, args *value.Map, ctl exec.BeaconControl) (any, error) {
				return changed(c, ctl.Delete(beaconName(args)))
			}),
		},
		exec.Module{
			Sig: sig("enable", "Let every beacon on this node fire again."),
			Fn: withBeacons(func(c *exec.Context, args *value.Map, ctl exec.BeaconControl) (any, error) {
				return changed(c, ctl.SetEnabled("", true))
			}),
		},
		exec.Module{
			Sig: sig("disable", "Hold every beacon on this node without forgetting any."),
			Fn: withBeacons(func(c *exec.Context, args *value.Map, ctl exec.BeaconControl) (any, error) {
				return changed(c, ctl.SetEnabled("", false))
			}),
		},
		exec.Module{
			Sig: sig("enable_beacon", "Let one beacon fire again.", name),
			Fn: withBeacons(func(c *exec.Context, args *value.Map, ctl exec.BeaconControl) (any, error) {
				return changed(c, ctl.SetEnabled(beaconName(args), true))
			}),
		},
		exec.Module{
			Sig: sig("disable_beacon", "Hold one beacon.", name),
			Fn: withBeacons(func(c *exec.Context, args *value.Map, ctl exec.BeaconControl) (any, error) {
				return changed(c, ctl.SetEnabled(beaconName(args), false))
			}),
		},
		exec.Module{
			Sig: sig("reset", "Remove every beacon from a running node."),
			Fn: withBeacons(func(c *exec.Context, args *value.Map, ctl exec.BeaconControl) (any, error) {
				return changed(c, ctl.Reset())
			}),
		},
		exec.Module{
			Sig: sig("save",
				"Write the running beacon configuration to disk, so it survives a "+
					"restart. It is written to a file of the node's own under "+
					"beacons.d, never over what a package manager put there."),
			Fn: withBeacons(func(c *exec.Context, args *value.Map, ctl exec.BeaconControl) (any, error) {
				if c.SaveConfig == nil {
					return nil, fmt.Errorf("this node has nowhere to write its beacons")
				}
				if c.Test {
					return value.MapOf("would_save", int64(ctl.Snapshot().Len())), nil
				}
				path, err := c.SaveConfig("beacons", ctl.Snapshot())
				if err != nil {
					return nil, err
				}
				return value.MapOf("saved", path), nil
			}),
		},
	)
}

// withBeacons refuses a management call on a node that is not running
// beacons, rather than reporting a change nobody made.
func withBeacons(fn func(*exec.Context, *value.Map, exec.BeaconControl) (any, error)) exec.Func {
	return func(c *exec.Context, args *value.Map) (any, error) {
		if c.Beacons == nil {
			return nil, fmt.Errorf(
				"this node is not running beacons, so there is nothing to change; " +
					"`beacons` in the configuration is what sets them")
		}
		return fn(c, args, c.Beacons)
	}
}

// changed is the answer a management call gives: true, or the reason.
func changed(c *exec.Context, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return true, nil
}

func beaconName(args *value.Map) string {
	return value.KeyString(mustArg(args, "name"))
}

// beaconConfig reads the configuration a caller passed: under Salt's
// `beacon_data` key, or as the remaining keyword arguments, which is
// how an operator types it.
func beaconConfig(args *value.Map) *value.Map {
	if raw, ok := args.Get("beacon_data"); ok && raw != nil {
		if m, ok := raw.(*value.Map); ok {
			return m
		}
		// A list is the file form, which an operator writing a
		// configuration by hand would reach for first.
		out := value.NewMap(1)
		out.Set("beacon_data", raw)
		return out
	}
	out := value.NewMap(args.Len())
	for _, e := range args.Entries() {
		key := value.KeyString(e.Key)
		if key == "name" || key == "beacon_data" || e.Val == nil {
			continue
		}
		out.Set(key, e.Val)
	}
	return out
}

// listBeacons answers from the registry and the configuration.
func listBeacons(c *exec.Context, args *value.Map) (any, error) {
	registry := beacon.New()
	if value.Truthy(mustArg(args, "available")) {
		out := value.NewMap(0)
		for _, name := range registry.Names() {
			mod, _ := registry.Lookup(name)
			entry := value.NewMap(3)
			entry.Set("doc", mod.Doc)
			if len(mod.Platforms) > 0 {
				entry.Set("platforms", anyList(mod.Platforms))
			}
			if mod.Pending != "" {
				entry.Set("pending", mod.Pending)
			}
			out.Set(name, entry)
		}
		return out, nil
	}

	// A running node answers from its engine, so a beacon added since
	// startup is in the list. One that is not answers from its
	// configuration, which is what a one-shot command line has.
	if c.Beacons != nil {
		return c.Beacons.List(), nil
	}
	configured, ok := c.Config.Get("beacons")
	if !ok || configured == nil {
		return value.NewMap(0), nil
	}
	instances, err := beacon.Parse(configured)
	if err != nil {
		return nil, err
	}
	out := value.NewMap(len(instances))
	for _, in := range instances {
		entry := value.NewMap(6)
		entry.Set("interval", in.Interval.String())
		if in.Delay > 0 {
			entry.Set("delay", in.Delay.String())
		}
		if in.OnChangeOnly {
			entry.Set("onchangeonly", true)
		}
		if in.DisableDuringStateRun {
			entry.Set("disable_during_state_run", true)
		}
		if in.Disabled {
			entry.Set("disabled", true)
		}
		entry.Set("config", in.Args)
		if !registry.Has(in.Name) {
			entry.Set("error", "this build has no beacon called "+in.Name)
		} else if mod, _ := registry.Lookup(in.Name); mod.Pending != "" {
			entry.Set("pending", mod.Pending)
		}
		out.Set(in.Name, entry)
	}
	return out, nil
}

func mustArg(args *value.Map, name string) any {
	v, _ := args.Get(name)
	return v
}

func anyList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
