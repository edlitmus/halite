package builtin

import (
	"fmt"
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

	r.States.Add(
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
	)
}
