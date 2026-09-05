package builtin

import (
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerSmallGaps adds the functions a real estate's tree reached for
// that this build had no answer for, and that each need one small thing.
//
// Grouped because they are one line of the migration report each and
// nothing else connects them. The larger ones — `pkgrepo`, `mount.mounted`,
// `state.apply` — are not here: each needs a platform or a subsystem, and
// putting them beside these would hide that.
func registerSmallGaps(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "grains", Function: "set",
				Doc: "Set a grain and persist it, as `grains.present` does. " +
					"For a reaction, which calls an execution function.",
				Params: []signature.Param{
					req("name", signature.String, "The grain."),
					req("value", signature.Any, "The value."),
					opt("delimiter", signature.String, ":",
						"Separator for a nested grain, as in `a:b:c`."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return asExecResult(grainsPresent(c, args))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "get_user",
				Doc:      "The owner of a path.",
				Params:   []signature.Param{req("path", signature.Path, "The path.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: fileGetUser,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "rename",
				Doc: "Move a path. Salt's name for what `file.move` also does, " +
					"kept because a tree writes one or the other.",
				Params: []signature.Param{
					req("src", signature.Path, "What to move."),
					req("dst", signature.Path, "Where to move it."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: fileRename,
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "event", Function: "send",
				Doc: "Fire an event onto the hub's bus as part of a run, so that " +
					"a state tree can announce what it did.",
				Params: []signature.Param{
					nameParam("The tag. Defaults to the state ID."),
					opt("data", signature.Map, nil, "The payload."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "17.3",
			},
			Fn: eventSendState,
		},
	)
}

func fileGetUser(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "path", "")
	if path == "" {
		return nil, fmt.Errorf("file.get_user needs a path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	// Through the same reader `file.stats` uses, which already knows
	// what a platform without uids does and falls back to the numeric id
	// where the account is not known to this machine — ordinary on a
	// node whose accounts come from a directory it cannot reach.
	owned := value.NewMap(4)
	addOwnership(owned, path, info)
	if name, ok := owned.Get("user"); ok {
		return name, nil
	}
	if uid, ok := owned.Get("uid"); ok {
		return fmt.Sprintf("%v", uid), nil
	}
	return nil, fmt.Errorf("%s has no owner this platform reports", path)
}

func fileRename(c *exec.Context, args *value.Map) (any, error) {
	src := states.Str(args, "src", "")
	dst := states.Str(args, "dst", "")
	if src == "" || dst == "" {
		return nil, fmt.Errorf("file.rename needs `src` and `dst`")
	}
	if c.Test {
		_, err := os.Lstat(src)
		return err == nil, nil
	}
	if err := os.Rename(src, dst); err != nil {
		return nil, err
	}
	return true, nil
}

// eventSendState fires an event from a state run.
//
// Always a change, never converged: an event is a thing that happened,
// not a condition to hold, so there is nothing for a second run to find
// already true. Salt's state does the same.
func eventSendState(c *exec.Context, args *value.Map) (states.Result, error) {
	tag := states.Str(args, "name", "")
	if tag == "" {
		return states.False("event.send needs a tag."), nil
	}
	if c.Events == nil {
		return states.False("This node has no hub to send an event to, so the " +
			"event would go nowhere. `event.send` needs the agent."), nil
	}

	changes := value.MapOf("event", states.Change(nil, tag))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The event %s would be sent.", tag), changes), nil
	}
	if _, err := sendEvent(c, value.MapOf("tag", tag, "data", mapArg(args, "data"))); err != nil {
		return states.False(fmt.Sprintf("The event %s could not be sent: %v", tag, err)), nil
	}
	return states.Changed(fmt.Sprintf("The event %s was sent.", tag), changes), nil
}

// mapArg reads an optional mapping argument.
func mapArg(args *value.Map, name string) *value.Map {
	v, ok := args.Get(name)
	if !ok || v == nil {
		return value.NewMap(0)
	}
	m, ok := v.(*value.Map)
	if !ok {
		return value.NewMap(0)
	}
	return m
}
