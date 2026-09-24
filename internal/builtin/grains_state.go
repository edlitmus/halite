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
				Doc: "Clear a grain: set it to null, or delete it outright with `destructive`.",
				Params: []signature.Param{
					nameParam("The grain. Defaults to the state ID."),
					opt("delimiter", signature.String, ":",
						"Separator for a nested grain, as in `a:b:c`."),
					opt("destructive", signature.Bool, false,
						"Delete the grain rather than setting it to null. Salt's default is false, "+
							"so the default here is too: `absent` usually means the name stays with no value."),
					opt("force", signature.Bool, false,
						"Clear a grain whose value is a list or a mapping. Without it those are refused, "+
							"because clearing a structure by accident loses more than a scalar does."),
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

// grainsAbsent clears a grain, as Salt's does.
//
// This used to refuse any grain the node had not set for itself, on the
// reasoning that a grain from the platform or the operator's file cannot
// be removed by editing the file this state owns, and that reporting a
// change the next run undoes is worse than refusing. The reasoning was
// sound about *deleting* and wrong about the state: Salt's `absent` does
// not delete by default, it sets the value to null, and a null written
// where this node's own grains live wins over the file underneath it --
// `99-runtime.yaml` is merged last precisely so that a runtime change
// beats the file it was made against.
//
// So the observable result matches Salt's without this state ever
// editing the operator's file. The estate that found it writes
// `grains.absent: node_exporter` against a grain its static file defines
// as null already, where Salt answers "Grain is already set" and this
// answered with a failure.
func grainsAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	if name == "" {
		return states.False("This state needs a grain name."), nil
	}
	path := grainPath(name, states.Str(args, "delimiter", ":"))
	destructive := states.Bool(args, "destructive", false)

	// The collected grains, which is what Salt reads: a grain is present
	// if the node reports it, wherever it came from.
	current, had := lookupGrain(c.Grains, path)
	if !had {
		return states.True(fmt.Sprintf("Grain %s does not exist.", name)), nil
	}

	// A structure is not cleared by accident. Salt refuses a list or a
	// mapping without `force` and names the argument that would allow it.
	if !states.Bool(args, "force", false) && isGrainCollection(current) {
		return states.False(fmt.Sprintf(
			"The key %q exists but is a dict or a list. Use `force: True` to overwrite.", name)), nil
	}

	held, err := heldGrains(c)
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be read back: %v", name, err)), nil
	}
	ourValue, ours := lookupGrain(held, path)

	if current == nil {
		// Already null, and nothing of this node's own to delete: there
		// is nothing to do. Salt says "Grain is already set" here, which
		// reads oddly and means "already in the state you asked for".
		if !destructive || !ours {
			return states.True(fmt.Sprintf("Grain %s is already set.", name)), nil
		}
		// `destructive` with an entry of our own that is *already the
		// null* is as far as a deletion can go, and saying so is what
		// stops this state oscillating for ever.
		//
		// It did oscillate. The apply path below deletes this node's
		// entry and then, where the grain is still visible from a file
		// this state does not own, writes a null to mask it -- and the
		// next run saw "null, and an entry of ours", deleted the mask,
		// uncovered the value, and masked it again. Measured over five
		// runs: mask, delete, mask, delete, mask, a change reported every
		// time. A state that cannot converge is worse than one that
		// fails, because nothing about it looks wrong.
		//
		// The key stays in the file, holding null. That is the honest end
		// state rather than a deletion: removing it would uncover the
		// value the operator asked to be rid of.
		if ourValue == nil {
			return states.True(fmt.Sprintf(
				"Grain %s is already null. Its value comes from a file this state does not "+
					"own, so a null of this node's own is as far as a deletion can go.", name)), nil
		}
	}

	changes := value.NewMap(1)
	if destructive {
		changes.Set("deleted", name)
	} else {
		changes.Set(name, states.Change(current, nil))
	}

	if c.Test {
		if destructive {
			return states.WouldChange(fmt.Sprintf("Grain %s is set to be deleted.", name), changes), nil
		}
		return states.WouldChange(
			fmt.Sprintf("Value for grain %s is set to be deleted (None).", name), changes), nil
	}

	if destructive {
		if _, err := deleteGrain(c, path); err != nil {
			return states.False(fmt.Sprintf("%s could not be deleted: %v", name, err)), nil
		}
		// Deleting this node's own entry uncovers whatever is underneath
		// it. Where that is the operator's file or the platform, the
		// grain is still there -- so it is masked with a null and said
		// so, rather than reporting a deletion that did not happen.
		if remaining, still := lookupGrain(c.Grains, path); still && remaining != nil && !ours {
			if _, err := saveGrain(c, path, nil); err != nil {
				return states.False(fmt.Sprintf("%s could not be cleared: %v", name, err)), nil
			}
			return states.Changed(fmt.Sprintf(
				"Grain %s was set to null rather than deleted: its value comes from a file "+
					"this state does not own, and a null here masks it.", name), changes), nil
		}
		return states.Changed(fmt.Sprintf("Grain %s was deleted.", name), changes), nil
	}

	if _, err := saveGrain(c, path, nil); err != nil {
		return states.False(fmt.Sprintf("%s could not be cleared: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("Value for grain %s was set to None.", name), changes), nil
}

// isGrainCollection reports whether a grain holds a structure rather
// than a scalar.
func isGrainCollection(v any) bool {
	switch v.(type) {
	case *value.Map, []any:
		return true
	}
	return false
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
	return persistGrains(c, held)
}

// deleteGrain removes a grain from the node's own grains file.
func deleteGrain(c *exec.Context, path []string) (string, error) {
	if c.SaveConfig == nil || c.ReloadConfig == nil {
		return "", fmt.Errorf("this invocation has nowhere to write grains; " +
			"a grain is persisted by the agent, not by a one-shot command")
	}
	held, err := heldGrains(c)
	if err != nil {
		return "", err
	}
	deleteNested(held, path)
	return persistGrains(c, held)
}

func persistGrains(c *exec.Context, held *value.Map) (string, error) {
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
	m.Set(last, want)
	return nil
}

// deleteNested removes a grain rather than setting it.
//
// Setting null and deleting are different outcomes -- `grains.absent`
// does the first by default and the second only with `destructive` --
// so they cannot share a nil argument. They used to, which made "set
// this grain to null" impossible to express.
func deleteNested(m *value.Map, path []string) {
	for _, part := range path[:len(path)-1] {
		child, ok := m.Get(part)
		if !ok {
			return
		}
		next, ok := child.(*value.Map)
		if !ok {
			return
		}
		m = next
	}
	m.Delete(path[len(path)-1])
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
