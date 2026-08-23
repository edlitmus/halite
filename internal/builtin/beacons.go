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
	notYet := func(function, doc string) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "beacons", Function: function, Doc: doc,
				AnyKwargs: true,
				TestMode:  signature.TestNotApplicable,
				Section:   "16.1",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return nil, fmt.Errorf(
					"beacons.%s changes a running node's watchers, which arrives with "+
						"the runtime management of SPEC section 16.1; `beacons` in the "+
						"configuration is what sets them today", function)
			},
		}
	}

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "beacons", Function: "list",
				Doc: "The beacons this node is configured to run, and what each is set to. " +
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
		notYet("add", "Add a beacon to a running node."),
		notYet("modify", "Change a running node's beacon."),
		notYet("delete", "Remove a beacon from a running node."),
		notYet("enable", "Enable beacons on a running node."),
		notYet("disable", "Disable beacons on a running node."),
		notYet("enable_beacon", "Enable one beacon on a running node."),
		notYet("disable_beacon", "Disable one beacon on a running node."),
		notYet("save", "Write the running beacon configuration to disk."),
		notYet("reset", "Drop every beacon from a running node."),
	)
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

	// What this node is configured to run. Read from the configuration
	// the context carries rather than from a running engine, so the
	// answer is the same whether the node is a daemon or a one-shot
	// command line.
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
