package builtin

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerEnviron installs the writing half of the environ module of SPEC
// section 15.2 and the environ.setenv state of 15.5. The reading half —
// get, has_value and items — is registered with the data module.
//
// Salt's environ.setval writes the agent process's own environment and
// nothing else, and its state does the same. Here that would be a state
// that reports a change and changes nothing an operator can observe:
// SPEC section 25.4 gives every child process a clean environment, so a
// variable set in the agent does not reach the next cmd.run, let alone
// survive a restart. So `permanent` defaults to true in this build, and
// the state manages the place the platform actually keeps the variable —
// /etc/environment on a unix, the environment key of the registry on
// Windows. That is the same reasoning sysctl.present is built on: the
// setting that is not persisted is not a setting, it is a note.
//
// The process copy is still written alongside it, so that environ.get
// later in the same run agrees with what was just declared.
func registerEnviron(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "environ", Function: "setval",
				Doc: "Set an environment variable, optionally where it survives a reboot.",
				Params: []signature.Param{
					req("key", signature.String, "The variable."),
					req("val", signature.String, "The value, or false with false_unsets to remove it."),
					opt("false_unsets", signature.Bool, false, "Treat a val of false as a removal rather than the string."),
					opt("permanent", signature.Bool, false, "Also write it where the platform keeps it across a reboot."),
					opt("scope", signature.String, "", environScopeDoc),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return environSetval(c, args)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "environ", Function: "setenv",
				Doc: "Set several environment variables at once.",
				Params: []signature.Param{
					req("environ", signature.Map, "The variables and their values."),
					opt("false_unsets", signature.Bool, false, "Treat a value of false as a removal rather than the string."),
					opt("permanent", signature.Bool, false, "Also write them where the platform keeps them across a reboot."),
					opt("scope", signature.String, "", environScopeDoc),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return environSetenvExec(c, args)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "environ", Function: "persisted",
				Doc: "Return the environment this node will come back up with.",
				Params: []signature.Param{
					opt("scope", signature.String, "", environScopeDoc),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				store, err := persistentEnviron(states.Str(args, "scope", ""))
				if err != nil {
					return nil, err
				}
				return store.Items()
			},
		},
	)

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "environ", Function: "setenv",
			Doc: "Ensure an environment variable has a value, and keeps it across a reboot.",
			Params: []signature.Param{
				nameParam("The variable. Defaults to the state ID, and is ignored when value is a mapping."),
				req("value", signature.Any, "The value, or a mapping of several variables to their values."),
				opt("false_unsets", signature.Bool, false, "Treat a value of false as a removal rather than the string."),
				opt("permanent", signature.Bool, true, "Manage the place the platform keeps the variable across a reboot. False makes this state affect only the agent's own process, as Salt's does."),
				opt("scope", signature.String, "", environScopeDoc),
				// Declared so that it is refused by name rather than
				// ignored. Salt's other option that this build does not
				// honour is not declared at all, because its name
				// carries a word SPEC section 2.3 prohibits; a tree
				// carrying it gets the unknown-argument error, which
				// names the file, the line and the key.
				opt("clear_all", signature.Bool, false, "Not accepted; see the comment this state returns."),
			},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.5",
		},
		Fn: environSetenvState,
	})
}

const environScopeDoc = "Whose environment: user or machine. Windows has both and defaults to user; a unix has only machine."

// environStore is the place a variable survives a reboot in.
type environStore interface {
	// Name is what the store is called, for a comment an operator reads.
	Name() string
	// Items returns everything the store holds.
	Items() (*value.Map, error)
	// Set writes one variable.
	Set(key, val string) error
	// Unset removes one, and is not an error when it was not there.
	Unset(key string) error
	// Flush tells the running system that the store changed, once, after
	// the last write. It is separate from Set because on Windows it is a
	// broadcast to every top-level window on the desktop, and doing that
	// per variable costs seconds a declaration of several would pay over
	// and over.
	Flush()
}

// EtcEnvironmentPath is where a unix keeps the system environment. A
// variable so a test can point it somewhere harmless.
var EtcEnvironmentPath = "/etc/environment"

// environSetval is the exec function.
func environSetval(c *exec.Context, args *value.Map) (any, error) {
	key := states.Str(args, "key", "")
	if key == "" {
		return nil, fmt.Errorf("setval needs a variable name")
	}
	raw, _ := args.Get("val")
	declared := value.MapOf(key, raw)
	changed, err := applyEnviron(c, declared, environOptions(args))
	if err != nil {
		return nil, err
	}
	if v, ok := changed.Get(key); ok {
		return v, nil
	}
	return raw, nil
}

func environSetenvExec(c *exec.Context, args *value.Map) (any, error) {
	declared, _ := args.Get("environ")
	m, ok := declared.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("setenv needs a mapping of variables to values")
	}
	return applyEnviron(c, m, environOptions(args))
}

// environOpts is what the caller asked for, gathered once.
type environOpts struct {
	falseUnsets bool
	permanent   bool
	scope       string
}

func environOptions(args *value.Map) environOpts {
	return environOpts{
		falseUnsets: states.Bool(args, "false_unsets", false),
		permanent:   states.Bool(args, "permanent", false),
		scope:       states.Str(args, "scope", ""),
	}
}

// applyEnviron writes the declared variables and returns what each ended
// up as, which for a removal is the empty string.
func applyEnviron(c *exec.Context, declared *value.Map, opts environOpts) (*value.Map, error) {
	var store environStore
	if opts.permanent {
		var err error
		if store, err = persistentEnviron(opts.scope); err != nil {
			return nil, err
		}
	}
	out := value.NewMap(declared.Len())
	for _, e := range declared.Entries() {
		key := value.KeyString(e.Key)
		val, remove := environValue(e.Val, opts.falseUnsets)
		if c.Test {
			out.Set(key, val)
			continue
		}
		if remove {
			if err := os.Unsetenv(key); err != nil {
				return nil, err
			}
			if store != nil {
				if err := store.Unset(key); err != nil {
					return nil, err
				}
			}
			out.Set(key, "")
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return nil, err
		}
		if store != nil {
			if err := store.Set(key, val); err != nil {
				return nil, err
			}
		}
		out.Set(key, val)
	}
	if store != nil && !c.Test {
		store.Flush()
	}
	return out, nil
}

// environValue renders a declared value and says whether it is a removal.
//
// Salt spells a removal as the boolean false with false_unsets set, and
// as the *string* "false" without it, which is a distinction a YAML tree
// cannot always make. Both spellings are honoured, and a nil is a
// removal either way: a key declared with no value is not a variable set
// to the word "None".
func environValue(raw any, falseUnsets bool) (val string, remove bool) {
	switch v := raw.(type) {
	case nil:
		return "", true
	case bool:
		if !v && falseUnsets {
			return "", true
		}
		if v {
			return "true", false
		}
		return "false", false
	case string:
		if falseUnsets && strings.EqualFold(v, "false") {
			return "", true
		}
		return v, false
	default:
		return fmt.Sprint(v), false
	}
}

func environSetenvState(c *exec.Context, args *value.Map) (states.Result, error) {
	if refusal := refusedEnvironOptions(args); refusal != "" {
		return states.False(refusal), nil
	}

	declared, err := declaredEnviron(args)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if declared.Len() == 0 {
		return states.False("This state needs a variable and a value."), nil
	}

	opts := environOptions(args)
	opts.permanent = states.Bool(args, "permanent", true)

	var store environStore
	if opts.permanent {
		if store, err = persistentEnviron(opts.scope); err != nil {
			return states.False(err.Error()), nil
		}
	}

	current := value.NewMap(0)
	where := "this agent's own environment"
	if store != nil {
		if current, err = store.Items(); err != nil {
			return states.False(fmt.Sprintf("%s could not be read: %v", store.Name(), err)), nil
		}
		where = store.Name()
	} else {
		for _, kv := range os.Environ() {
			if k, v, ok := strings.Cut(kv, "="); ok {
				current.Set(k, v)
			}
		}
	}

	changes := value.NewMap(declared.Len())
	for _, e := range declared.Entries() {
		key := value.KeyString(e.Key)
		want, remove := environValue(e.Val, opts.falseUnsets)
		have, held := current.Get(key)
		switch {
		case remove && held:
			changes.Set(key, states.Change(have, nil))
		case remove:
			// Not there and not wanted.
		case !held:
			changes.Set(key, states.Change(nil, want))
		case value.KeyString(have) != want:
			changes.Set(key, states.Change(have, want))
		}
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf(
			"%s already holds what was declared for %s.", where, environSummary(declared))), nil
	}
	if c.Test {
		return states.WouldChange(environComment(changes, where, true), changes), nil
	}
	if _, err := applyEnviron(c, declared, opts); err != nil {
		return states.False(fmt.Sprintf("%s could not be written: %v", where, err)), nil
	}
	return states.Changed(environComment(changes, where, false), changes), nil
}

// environComment says what changed, keeping a removal a removal: "was
// set" over a variable that was deleted is the kind of report an
// operator reads once, believes, and is wrong about.
func environComment(changes *value.Map, where string, future bool) string {
	set, removed := value.NewMap(changes.Len()), value.NewMap(changes.Len())
	for _, e := range changes.Entries() {
		if m, ok := e.Val.(*value.Map); ok {
			if to, _ := m.Get("new"); to == nil {
				removed.Set(e.Key, e.Val)
				continue
			}
		}
		set.Set(e.Key, e.Val)
	}

	// "was" over two variables reads as a mistake and makes the rest of
	// the sentence harder to trust.
	verb := func(m *value.Map, past string) string {
		if future {
			return "would be " + past
		}
		if m.Len() == 1 {
			return "was " + past
		}
		return "were " + past
	}
	switch {
	case removed.Len() == 0:
		return fmt.Sprintf("%s %s in %s.", environSummary(set), verb(set, "set"), where)
	case set.Len() == 0:
		return fmt.Sprintf("%s %s from %s.", environSummary(removed), verb(removed, "removed"), where)
	default:
		return fmt.Sprintf("%s %s in %s, and %s %s from it.",
			environSummary(set), verb(set, "set"), where,
			environSummary(removed), verb(removed, "removed"))
	}
}

// declaredEnviron gathers what the state asked for, from either spelling:
// a name and a value, or a mapping under value.
func declaredEnviron(args *value.Map) (*value.Map, error) {
	raw, ok := args.Get("value")
	if !ok {
		return nil, fmt.Errorf("This state needs a value.")
	}
	if m, ok := raw.(*value.Map); ok {
		return m, nil
	}
	name := states.Str(args, "name", "")
	if name == "" {
		return nil, fmt.Errorf("This state needs a variable name.")
	}
	return value.MapOf(name, raw), nil
}

// refusedEnvironOptions names the Salt option this build does not take,
// and says why, rather than accepting it and doing something else.
func refusedEnvironOptions(args *value.Map) string {
	if states.Bool(args, "clear_all", false) {
		return "clear_all: True is not accepted: in Salt it empties the agent's " +
			"whole environment, and the variables it would remove here are " +
			"the ones this agent is running on. Declare the removals."
	}
	return ""
}

// environSummary names what changed, in a fixed order so that two runs
// reporting the same thing read the same.
func environSummary(m *value.Map) string {
	names := make([]string, 0, m.Len())
	for _, e := range m.Entries() {
		names = append(names, value.KeyString(e.Key))
	}
	sort.Strings(names)
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
