package builtin

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// registerGrainsStates adds the grains state module of SPEC 15.5.
//
// Every grains *execution* function shipped and there was no grains
// state, so a tree that sets a grain declaratively — `role: web` on the
// machine that is a web server — had nowhere to put it. It was the
// largest single gap in a real estate's tree at eleven references.
//
// The value is written to `grains.d/99-runtime.yaml`, which the node
// already merges, so a grain set by a state lands where one set by a
// package or by hand does.
func registerGrainsStates(r *Registries) {
	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "grains", Function: "present",
				Doc: "Ensure a grain has a value, writing it where the node reads its own grains.",
				Params: []signature.Param{
					nameParam("The grain. Defaults to the state ID."),
					req("value", signature.Any, "The value to set."),
					opt("delimiter", signature.String, ":",
						"Separator for a nested grain, as in `a:b:c`."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: grainsPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "grains", Function: "absent",
				Doc: "Ensure a grain this node set for itself is gone.",
				Params: []signature.Param{
					nameParam("The grain. Defaults to the state ID."),
					opt("delimiter", signature.String, ":",
						"Separator for a nested grain, as in `a:b:c`."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: grainsAbsent,
		},
	)
}

// grainsPresent sets a grain and persists it.
func grainsPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	if name == "" {
		return states.False("This state needs a grain name."), nil
	}
	want, ok := args.Get("value")
	if !ok {
		return states.False(fmt.Sprintf(
			"grains.present needs a value for %s. To remove a grain, use grains.absent.", name)), nil
	}
	path := grainPath(name, states.Str(args, "delimiter", ":"))

	current, had := lookupGrain(c.Grains, path)
	if had && sameGrain(current, want) {
		return states.True(fmt.Sprintf("%s is already %v.", name, want)), nil
	}

	changes := value.NewMap(1)
	changes.Set(name, states.Change(current, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be set to %v.", name, want), changes), nil
	}

	written, err := saveGrain(c, path, want)
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be set: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was set to %v in %s.", name, want, written), changes), nil
}

// grainsAbsent removes a grain this node set for itself.
func grainsAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	if name == "" {
		return states.False("This state needs a grain name."), nil
	}
	path := grainPath(name, states.Str(args, "delimiter", ":"))

	current, had := lookupGrain(c.Grains, path)

	// Whether this is removable is decided from the file this state
	// owns, not from the collected grains, and before anything is
	// written. A grain that came from the platform or from the
	// configuration file is not removable by editing that file, and
	// reporting a change the next run finds undone is worse than
	// refusing.
	//
	// Checking c.Grains afterwards does not work: it is the snapshot the
	// job started with, and reloading updates the node's grains rather
	// than this copy — so every removal looked like it had failed.
	held, err := heldGrains(c)
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be read back: %v", name, err)), nil
	}
	_, ours := lookupGrain(held, path)

	if !ours {
		if !had {
			return states.True(fmt.Sprintf("%s is not set.", name)), nil
		}
		return states.False(fmt.Sprintf(
			"%s is not a grain this node set for itself, so there is nothing here "+
				"to remove. It comes from the platform or from the configuration "+
				"file, and neither is this state's to edit.", name)), nil
	}

	changes := value.NewMap(1)
	changes.Set(name, states.Change(current, nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be removed.", name), changes), nil
	}

	written, err := saveGrain(c, path, nil)
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be removed: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was removed from %s.", name, written), changes), nil
}

// grainPath splits a delimited grain name into its parts.
func grainPath(name, delimiter string) []string {
	if delimiter == "" {
		delimiter = ":"
	}
	return strings.Split(name, delimiter)
}

// lookupGrain walks a nested grain path.
func lookupGrain(grains *value.Map, path []string) (any, bool) {
	if grains == nil {
		return nil, false
	}
	var current any = grains
	for _, part := range path {
		m, ok := current.(*value.Map)
		if !ok {
			return nil, false
		}
		current, ok = m.Get(part)
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// saveGrain writes one grain into the node's own grains file, merging
// with what that file already holds. A nil value removes it.
func saveGrain(c *exec.Context, path []string, want any) (string, error) {
	if c.SaveConfig == nil || c.ReloadConfig == nil {
		return "", fmt.Errorf("this invocation has nowhere to write grains; " +
			"a grain is persisted by the agent, not by a one-shot command")
	}

	held, err := heldGrains(c)
	if err != nil {
		return "", err
	}

	if err := setNested(held, path, want); err != nil {
		return "", err
	}
	written, err := c.SaveConfig("grains", held)
	if err != nil {
		return "", err
	}
	if err := c.ReloadConfig("grains"); err != nil {
		return written, err
	}
	return written, nil
}

// setNested assigns into a nested mapping, creating the intermediate
// maps a path needs. A nil value deletes.
func setNested(m *value.Map, path []string, want any) error {
	for i, part := range path[:len(path)-1] {
		next, ok := m.Get(part)
		if !ok {
			child := value.NewMap(2)
			m.Set(part, child)
			m = child
			continue
		}
		child, ok := next.(*value.Map)
		if !ok {
			return fmt.Errorf("%s is a value, not a mapping, so %s cannot be set under it",
				strings.Join(path[:i+1], ":"), strings.Join(path, ":"))
		}
		m = child
	}
	last := path[len(path)-1]
	if want == nil {
		m.Delete(last)
		return nil
	}
	m.Set(last, want)
	return nil
}

// sameGrain reports whether two grain values are the same.
//
// Compared through the project's own YAML encoding rather than with
// reflect: a grain read back from `grains.d` has been through the
// parser, so `8` from the file and `8` from a template are the same
// number written two ways, and the encoding is what makes them equal.
func sameGrain(a, b any) bool {
	return encodeGrain(a) == encodeGrain(b)
}

func encodeGrain(v any) string {
	return yaml.Encode(value.MapOf("v", v), yaml.EncodeOptions{Indent: 2})
}

// heldGrains reads the grains this node has set for itself, which is the
// only set a grains state may change. An absent file is an empty
// mapping.
func heldGrains(c *exec.Context) (*value.Map, error) {
	if c.LoadConfig == nil {
		return value.NewMap(0), nil
	}
	held, err := c.LoadConfig("grains")
	if err != nil {
		return nil, err
	}
	if held == nil {
		return value.NewMap(0), nil
	}
	return held, nil
}
