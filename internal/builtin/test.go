package builtin

import (
	"fmt"
	"reflect"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerTest installs the test module. It changes nothing, which is what
// makes it the module every fleet reaches for first: `test.ping` answers
// "is this node there and does the whole path work" without touching it.
func registerTest(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "test", Function: "ping",
				Doc:      "Return true. Confirms a node is reachable and its executor works.",
				Returns:  "true",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return true, nil },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "test", Function: "echo",
				Doc:      "Return the argument unchanged.",
				Params:   []signature.Param{req("text", signature.Any, "What to return.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				v, _ := args.Get("text")
				return v, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "test", Function: "version",
				Doc:      "Return this node's halite version.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				v, _ := c.Grains.Get("haliteversion")
				return v, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "test", Function: "sleep",
				Doc:      "Sleep for a number of seconds, then return true.",
				Params:   []signature.Param{opt("length", signature.Int, int64(1), "Seconds to sleep.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				n := states.Int(args, "length", 1)
				select {
				case <-time.After(time.Duration(n) * time.Second):
					return true, nil
				case <-c.Ctx.Done():
					return nil, c.Ctx.Err()
				}
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "test", Function: "raise_error",
				Doc:      "Fail on purpose, for exercising failure paths.",
				Params:   []signature.Param{opt("message", signature.String, "raised on request", "The error text.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return nil, fmt.Errorf("%s", states.Str(args, "message", "raised on request"))
			},
		},
	)

	nop := func(c *exec.Context, args *value.Map) (states.Result, error) {
		return states.True("This state does nothing, successfully."), nil
	}

	testStates := []states.Module{
		states.Module{
			Sig: signature.Signature{
				Module: "test", Function: "nop",
				Doc:      "Do nothing and succeed. Useful as a requisite anchor.",
				Params:   []signature.Param{nameParam("Ignored.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.5",
			},
			Fn: nop,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "test", Function: "show_notification",
				Doc: "Succeed and put a message in the run's output. " +
					"Changes nothing.",
				Params: []signature.Param{
					nameParam("Ignored."),
					req("text", signature.String, "The message to show."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				text := states.Str(args, "text", "")
				if text == "" {
					return states.False("test.show_notification needs `text`, " +
						"which is the whole of what it does."), nil
				}
				return states.True(text), nil
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "test", Function: "succeed_without_changes",
				Doc:      "Succeed and report no changes.",
				Params:   []signature.Param{nameParam("Ignored.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.5",
			},
			Fn: nop,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "test", Function: "fail_without_changes",
				Doc: "Fail and report no changes, for exercising onfail.",
				Params: []signature.Param{
					nameParam("Ignored."),
					opt("comment", signature.String, "Failure is on purpose.", "The failure comment."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return states.False(states.Str(args, "comment", "Failure is on purpose.")), nil
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "test", Function: "succeed_with_changes",
				Doc:      "Succeed and report a synthetic change, for exercising watch and onchanges.",
				Params:   []signature.Param{nameParam("Ignored.")},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				changes := value.MapOf("testing", states.Change("unchanged", "changed"))
				if c.Test {
					return states.WouldChange("This state would report a synthetic change.", changes), nil
				}
				return states.Changed("This state reported a synthetic change.", changes), nil
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "test", Function: "configurable_test_state",
				Doc: "Return a chosen result and change set, for exercising requisite behaviour.",
				Params: []signature.Param{
					nameParam("Ignored."),
					opt("changes", signature.Bool, true, "Whether to report changes."),
					opt("result", signature.Bool, true, "The result to report."),
					opt("comment", signature.String, "", "The comment to report."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				wantChanges := states.Bool(args, "changes", true)
				wantResult := states.Bool(args, "result", true)
				comment := states.Str(args, "comment", "")

				changes := value.NewMap(0)
				if wantChanges {
					changes = value.MapOf("testing", states.Change("unchanged", "changed"))
				}
				switch {
				case c.Test && wantChanges:
					if comment == "" {
						comment = "This state would change something."
					}
					return states.WouldChange(comment, changes), nil
				case !wantResult:
					if comment == "" {
						comment = "This state was configured to fail."
					}
					out := states.False(comment)
					out.Changes = changes
					return out, nil
				default:
					if comment == "" {
						comment = "This state was configured to succeed."
					}
					return states.Changed(comment, changes), nil
				}
			},
		},
	}
	// Every test state has Salt's mod_watch, as every state in Salt's
	// test module does, since it is one function for the module.
	for i := range testStates {
		testStates[i].ModWatch = testModWatch(testStates[i])
	}
	r.States.Add(testStates...)
}

// testModWatch is Salt's test.mod_watch, called in Salt's order.
//
// When a watch fires, Salt runs the state's function first and calls
// mod_watch only when that reported no changes, a failure included; a
// listen reaction goes to mod_watch directly. Salt's test.mod_watch
// succeeds with "Watch statement fired." and lists the watched states
// that changed, and `result`, `comment` and `changes` written on the
// state replace those. halite's test states had no mod_watch, so a
// fired watch ran the state as if nothing had: captured from Salt
// 3006.28 and 3008.3, `watch_in: [sls: g1]` from a state with changes
// made g1's unchanged state report "Watch statement fired." and
// {"Requisites with changes": ["test: a"]}, where halite reported it
// had done nothing. DIVERGENCE 5.478.
//
// Salt's mod_watch sees only what was written on the state; halite's
// arguments arrive bound, defaults filled in, so a value equal to its
// parameter's default is read as not written. fail_without_changes's
// default comment would otherwise replace "Watch statement fired.", and
// configurable_test_state's empty one would blank it. Writing the
// default explicitly is the case this gets wrong. `changes` is not
// taken: Salt puts whatever was written in as the change set, and
// configurable_test_state takes it as a boolean, which as a change set
// means nothing.
func testModWatch(m states.Module) states.Func {
	written := func(args *value.Map, name string) (any, bool) {
		v, ok := args.Get(name)
		if !ok {
			return nil, false
		}
		for _, p := range m.Sig.Params {
			if p.Name == name && p.Default != nil && reflect.DeepEqual(v, p.Default) {
				return nil, false
			}
		}
		return v, true
	}
	return func(c *exec.Context, args *value.Map) (states.Result, error) {
		if c.Reaction != exec.ReactionListen {
			res, err := m.Fn(c, args)
			if err != nil || res.HasChanges() {
				return res, err
			}
		}
		result := true
		if v, ok := written(args, "result"); ok {
			result = value.Truthy(v)
		}
		comment := "Watch statement fired."
		if v, ok := written(args, "comment"); ok {
			comment = value.KeyString(v)
		}
		watched := make([]any, len(c.WatchChanged))
		for i, w := range c.WatchChanged {
			watched[i] = w
		}
		out := states.True(comment)
		if !result {
			out = states.False(comment)
		}
		out.Changes = value.MapOf("Requisites with changes", watched)
		return out, nil
	}
}
