package builtin

import (
	"fmt"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// win_service, SPEC section 15.3.
//
// The virtual `service` module covers what every init system has —
// start, stop, enabled, get_all — and the service control manager is now
// a provider behind it, so `service.running` works on this platform.
// What is here is what the virtual module has no place for, because no
// other init system has it: a start type with four values rather than
// enabled-or-not, the account a service runs as, its binary path, its
// display name, and the process it is running in.
//
// `service.enabled` therefore says less on Windows than a tree may need.
// It answers "does the machine bring this up", which is true for both
// automatic and delayed-automatic start; a tree that cares which one
// says so here.

func registerWinService(r *Registries) {
	nameOnly := []signature.Param{req("name", signature.String,
		"The service name, as the manager knows it — not the display name the console shows.")}

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "win_service", Function: "info",
				Doc:    "Return everything the service control manager knows about a service.",
				Params: nameOnly,
				Returns: "a mapping with name, display_name, state, start_type, pid, " +
					"binary_path, account and description",
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winServiceInfo(states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_service", Function: "get_start_type",
				Doc: "Return when a service starts: auto, auto_delayed, manual, disabled, " +
					"boot or system.",
				Params:    nameOnly,
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winServiceStartType(states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_service", Function: "set_start_type",
				Doc: "Set when a service starts.",
				Params: []signature.Param{
					nameOnly[0],
					choice("start_type", "auto", "When the service should start.",
						"auto", "auto_delayed", "manual", "disabled"),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := winServiceSetStartType(
					states.Str(args, "name", ""), states.Str(args, "start_type", "auto"))
				if err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_service", Function: "list",
				Doc: "Return every service with its state and process, which is one call " +
					"rather than one per service.",
				Returns:   "a list of mappings with name, display_name, state and pid",
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winServiceList()
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "win_service", Function: "start_type",
				Doc: "Ensure a service starts when the declaration says it should.",
				Params: []signature.Param{
					nameParam("The service. Defaults to the state ID."),
					choice("start_type", "auto", "When the service should start.",
						"auto", "auto_delayed", "manual", "disabled"),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.5",
			},
			Fn: winServiceStartTypeState,
		},
	)
}

// winServiceStartTypeState ensures a service's start type.
//
// A separate state from `service.enabled` because the two questions are
// not the same here. `service.enabled` is a boolean and this is one of
// four values; a tree that wants delayed automatic start cannot say so
// with a boolean, and one that wants `manual` rather than `disabled`
// cannot either.
func winServiceStartTypeState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	want := states.Str(args, "start_type", "auto")
	if name == "" {
		return states.False("win_service.start_type needs a service name."), nil
	}

	current, err := winServiceStartType(name)
	if err != nil {
		return states.False(fmt.Sprintf("The start type of %s could not be read: %v", name, err)), nil
	}
	if current == want {
		return states.True(fmt.Sprintf("%s already starts %s.", name, want)), nil
	}

	changes := value.NewMap(1)
	changes.Set("start_type", states.Change(current, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would start %s rather than %s.",
			name, want, current), changes), nil
	}
	if err := winServiceSetStartType(name, want); err != nil {
		return states.False(fmt.Sprintf("The start type of %s could not be set to %s: %v",
			name, want, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s now starts %s rather than %s.",
		name, want, current), changes), nil
}
