package hub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// orchStates builds the step modules of SPEC 19.1 for one run.
//
// They are `salt.*` rather than something renamed, because SPEC 19.1
// keeps Salt's orchestration syntax so that an existing orchestration
// runs unchanged. SPEC 2.3 retains the word salt in the vocabulary; it
// is the role names that are prohibited.
func orchStates(o *orchRunner) *states.Registry {
	r := states.NewRegistry()

	fleetParams := func(extra ...signature.Param) []signature.Param {
		base := []signature.Param{
			{Name: "name", Type: signature.String, Doc: "The step name; defaults to the step's ID."},
			{Name: "tgt", Type: signature.String, Required: true, Doc: "Which nodes."},
			{Name: "tgt_type", Type: signature.String, Doc: "The target kind of SPEC section 8, such as G or C."},
			{Name: "saltenv", Type: signature.String, Doc: "The environment the job names."},
			{Name: "test", Type: signature.Bool, Doc: "Run the job in test mode."},
			{Name: "batch", Type: signature.String, Doc: "Run against this many nodes at a time, as a count or a percentage."},
			{Name: "batch_safe_limit", Type: signature.Int, Doc: "Stop the batch once this many nodes have failed."},
			{Name: "subset", Type: signature.Int, Doc: "Run against a random n of the matched set."},
			{Name: "tolerate_failures", Type: signature.List,
				Doc: "Node globs whose failure does not fail the step. Salt calls this fail_minions."}, // lexicon:allow
			{Name: "fail_minions", Type: signature.List, // lexicon:allow
				Doc: "The old name for tolerate_failures, accepted for an unchanged orchestration."},
			{Name: "queue", Type: signature.Any,
				Doc: "Hold the step on the hub's durable queue. Not built; see SPEC section 19.4."},
			{Name: "ret", Type: signature.String,
				Ineffective: "an orchestration step does not route its return through a returner; " +
					"configure `returner` on the hub for that (SPEC section 20.3). " +
					"The return is in the job cache either way"},
		}
		return append(base, extra...)
	}

	r.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "salt", Function: "state",
				Doc: "Apply state on the matched nodes and wait for the returns. " +
					"With no `sls` it is a highstate.",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "19.1",
				Params: fleetParams(
					signature.Param{Name: "sls", Type: signature.List, Doc: "The SLS names to apply."},
					signature.Param{Name: "highstate", Type: signature.Bool, Doc: "Apply the top file instead of named SLS."},
					signature.Param{Name: "pillar", Type: signature.Map, Doc: "Pillar overrides for this run."},
					signature.Param{Name: "pillarenv", Type: signature.String, Doc: "The pillar environment the job names."},
				),
			},
			Fn: o.applyState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "salt", Function: "sls",
				Doc:      "Apply named SLS on the matched nodes. The same as salt.state with `sls`.",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "19.1",
				Params: fleetParams(
					signature.Param{Name: "sls", Type: signature.List, Doc: "The SLS names to apply."},
					signature.Param{Name: "pillar", Type: signature.Map, Doc: "Pillar overrides for this run."},
					signature.Param{Name: "pillarenv", Type: signature.String, Doc: "The pillar environment the job names."},
				),
			},
			Fn: o.applyState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "salt", Function: "highstate",
				Doc:      "Apply the top file on the matched nodes.",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "19.1",
				Params: fleetParams(
					signature.Param{Name: "pillar", Type: signature.Map, Doc: "Pillar overrides for this run."},
					signature.Param{Name: "pillarenv", Type: signature.String, Doc: "The pillar environment the job names."},
				),
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				args.Set("highstate", true)
				return o.applyState(c, args)
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "salt", Function: "function",
				Doc: "Run an execution function on the matched nodes and wait for the returns. " +
					"`name` is the function.",
				Mutates: true,
				// A step runs whatever function it names, and this
				// build cannot know whether that function honours test
				// mode. Saying so is better than claiming it does.
				TestMode:      signature.TestUnreliable,
				ArbitraryCode: true,
				Section:       "19.1",
				Params: fleetParams(
					signature.Param{Name: "arg", Type: signature.List, Doc: "Positional arguments."},
					signature.Param{Name: "kwarg", Type: signature.Map, Doc: "Keyword arguments."},
				),
			},
			Fn: o.callFunction,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "salt", Function: "runner",
				Doc:      "Call a runner on the hub. `name` is the runner.",
				Mutates:  true,
				TestMode: signature.TestUnreliable,
				Section:  "19.1",
				Params: []signature.Param{
					{Name: "name", Type: signature.String, Doc: "The runner, as module.function."},
					{Name: "arg", Type: signature.List, Doc: "Positional arguments."},
					{Name: "kwarg", Type: signature.Map, Doc: "Keyword arguments."},
				},
			},
			Fn: o.callRunner,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "salt", Function: "wheel",
				Doc: "Call a hub management function. This build has one hub-function " +
					"namespace rather than Salt's two, so a wheel step reaches the same " +
					"registry a runner step does.",
				Mutates:  true,
				TestMode: signature.TestUnreliable,
				Section:  "19.3",
				Params: []signature.Param{
					{Name: "name", Type: signature.String, Doc: "The function, as module.function."},
					{Name: "arg", Type: signature.List, Doc: "Positional arguments."},
					{Name: "kwarg", Type: signature.Map, Doc: "Keyword arguments."},
				},
			},
			Fn: o.callRunner,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "salt", Function: "wait_for_event",
				Doc: "Block until an event matching the tag arrives, or the step's " +
					"`timeout` expires. This is what lets an orchestration coordinate " +
					"with something outside the estate rather than only push work outward.",
				Mutates:  false,
				TestMode: signature.TestReliable,
				Section:  "19.1",
				Params: []signature.Param{
					{Name: "name", Type: signature.String, Doc: "The tag glob to wait for."},
					{Name: "tag", Type: signature.String, Doc: "The tag glob, if not the name."},
				},
			},
			Fn: o.waitForEvent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "salt", Function: "parallel",
				Doc:      "Run several steps at once.",
				Mutates:  true,
				TestMode: signature.TestNotApplicable,
				Section:  "19.1",
				Params: []signature.Param{
					{Name: "name", Type: signature.String, Doc: "The step name."},
				},
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return states.False("`salt.parallel` is not built: this build runs a " +
					"low state in one order, one step at a time. DIVERGENCE 4.4."), nil
			},
		},
	)
	return r
}

// applyState backs salt.state, salt.sls, and salt.highstate.
func (o *orchRunner) applyState(c *exec.Context, args *value.Map) (states.Result, error) {
	sub, res, stop := o.submission(c, args, "state.apply")
	if stop {
		return res, nil
	}

	if !states.Bool(args, "highstate", false) {
		sub.Arg = states.Strings(args, "sls")
	}
	// A state honours test mode by contract -- SPEC 11.6, enforced for
	// every state module by the conformance harness -- so a test run
	// dispatches the job with `test` set and reports what each node
	// would do. That is the useful answer, and it is safe.
	sub.Test = sub.Test || c.Test
	if p := states.Mapping(args, "pillar"); p != nil && p.Len() > 0 {
		sub.Kwarg = map[string]any{"pillar": p}
	}
	if env := states.Str(args, "pillarenv", ""); env != "" {
		if sub.Kwarg == nil {
			sub.Kwarg = map[string]any{}
		}
		sub.Kwarg["pillarenv"] = env
	}
	return o.dispatchAndWait(c, args, sub, true)
}

// callFunction backs salt.function.
func (o *orchRunner) callFunction(c *exec.Context, args *value.Map) (states.Result, error) {
	fun := states.Str(args, "name", "")
	if fun == "" {
		return states.False("A salt.function step needs `name`, the function to run."), nil
	}
	sub, res, stop := o.submission(c, args, fun)
	if stop {
		return res, nil
	}
	for _, a := range states.Strings(args, "arg") {
		sub.Arg = append(sub.Arg, a)
	}
	if kw := states.Mapping(args, "kwarg"); kw != nil && kw.Len() > 0 {
		sub.Kwarg = map[string]any{}
		for _, e := range kw.Entries() {
			sub.Kwarg[value.KeyString(e.Key)] = e.Val
		}
	}
	return o.dispatchAndWait(c, args, sub, false)
}

// submission builds the common half of a fleet step, and reports the
// refusals that stop one before it is dispatched.
func (o *orchRunner) submission(c *exec.Context, args *value.Map, fun string) (Submission, states.Result, bool) {
	if raw, ok := args.Get("queue"); ok && raw != nil && value.Truthy(raw) {
		return Submission{}, states.False(
			"`queue` holds a step on the hub's durable queue, which is not built; " +
				"it arrives with the queue runner (SPEC section 19.4)."), true
	}
	env := states.Str(args, "saltenv", "")
	if env == "" {
		env = o.env
	}
	sub := Submission{
		Target:     states.Str(args, "tgt", ""),
		TargetKind: states.Str(args, "tgt_type", ""),
		Fun:        fun,
		Env:        env,
		Test:       states.Bool(args, "test", false),
		BatchSpec:  states.Str(args, "batch", ""),
		Subset:     int(states.Int(args, "subset", 0)),
		Batch: job.Batch{
			SafeLimit: int(states.Int(args, "batch_safe_limit", 0)),
		},
	}
	if sub.Target == "" {
		return Submission{}, states.False("A fleet step needs `tgt`, the nodes to run against."), true
	}
	return sub, states.Result{}, false
}

// dispatchAndWait sends the job, waits for it, and turns the returns
// into the step's result.
// honoursTest says whether the job this step sends can be trusted to
// change nothing under test mode. A state can, by the contract of SPEC
// 11.6. An execution function cannot: `salt.function` runs whatever it
// names, and running `pkg.install` with `test=True` against the fleet
// to find out what a test run would do is how a test run becomes a
// deployment.
func (o *orchRunner) dispatchAndWait(c *exec.Context, args *value.Map, sub Submission, honoursTest bool) (states.Result, error) {
	if c.Test && !honoursTest {
		return states.WouldChange(
			fmt.Sprintf("Would run %s on the nodes matching %q.", sub.Fun, sub.Target), nil), nil
	}

	j, err := o.server.DispatchAs(o.principal, sub)
	if err != nil {
		return states.False(err.Error()), nil
	}
	o.server.emit(tagOrchStep(string(o.jid), c.StateID), "", map[string]any{
		"jid": string(o.jid), "step": c.StateID, "job_jid": string(j.JID),
		"fun": sub.Fun, "target": sub.Target, "nodes": len(j.Nodes),
	})

	timeout := stepTimeout(c)
	_, returns, missing, err := o.server.awaitReturns(c.Ctx, j.JID, timeout)
	if err != nil {
		return states.False(fmt.Sprintf("Waiting for %s: %v", j.JID, err)), nil
	}
	// Re-read the job: a batched one has been changing while we waited.
	current, err := o.server.Jobs.Get(j.JID)
	if err != nil {
		return states.False(err.Error()), nil
	}
	return stepOutcome(o, c.StateID, current, returns, missing, tolerated(args)), nil
}

// callRunner backs salt.runner and salt.wheel.
func (o *orchRunner) callRunner(c *exec.Context, args *value.Map) (states.Result, error) {
	fun := states.Str(args, "name", "")
	if fun == "" {
		return states.False("A runner step needs `name`, the runner to call."), nil
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("Would call the runner %s.", fun), nil), nil
	}

	var arg []string
	arg = append(arg, states.Strings(args, "arg")...)
	kwarg := map[string]any{}
	if kw := states.Mapping(args, "kwarg"); kw != nil {
		for _, e := range kw.Entries() {
			kwarg[value.KeyString(e.Key)] = e.Val
		}
	}

	out, err := o.server.CallRunner(c.Ctx, RunnerCall{
		Principal: o.principal,
		Fun:       fun,
		Arg:       arg,
		Kwarg:     kwarg,
	})
	if err != nil {
		return states.False(err.Error()), nil
	}
	detail := o.detail(c.StateID)
	detail.JID = string(out.JID)
	if !out.Success {
		return states.False(fmt.Sprintf("%s: %s", fun, out.Err)), nil
	}
	changes := value.NewMap(1)
	changes.Set("return", out.Return)
	return states.Result{
		Result: boolPtr(true), Changes: changes,
		Comment: fmt.Sprintf("%s ran on the hub.", fun),
	}, nil
}

// waitForEvent backs salt.wait_for_event.
func (o *orchRunner) waitForEvent(c *exec.Context, args *value.Map) (states.Result, error) {
	tag := states.Str(args, "tag", "")
	if tag == "" {
		tag = states.Str(args, "name", "")
	}
	if tag == "" {
		return states.False("A wait_for_event step needs a tag to wait for."), nil
	}
	if o.server.Events == nil {
		return states.False("This hub keeps no event bus, so nothing can be waited for."), nil
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("Would wait for an event matching %q.", tag), nil), nil
	}

	bus := o.server.Events
	globs := tagGlobs(tag)
	deadline := time.NewTimer(stepTimeout(c))
	defer deadline.Stop()

	// From latest: a step waits for what happens next, and starting at
	// the beginning of the log would satisfy it with history.
	from := "latest"
	for {
		wake := bus.Wait()
		events, next, err := bus.Read(from, globs, 1)
		if err != nil {
			return states.False(err.Error()), nil
		}
		if len(events) > 0 {
			changes := value.NewMap(1)
			changes.Set("event", eventSummary(events[0]))
			return states.Result{
				Result: boolPtr(true), Changes: changes,
				Comment: fmt.Sprintf("An event matching %q arrived: %s.", tag, events[0].Tag),
			}, nil
		}
		from = next
		select {
		case <-wake:
		case <-deadline.C:
			return states.False(fmt.Sprintf("No event matching %q arrived within the timeout.", tag)), nil
		case <-c.Ctx.Done():
			if errors.Is(c.Ctx.Err(), context.DeadlineExceeded) {
				return states.False(fmt.Sprintf(
					"No event matching %q arrived within the step's timeout.", tag)), nil
			}
			return states.False("The run was stopped while waiting for an event."), nil
		}
	}
}

func eventSummary(e eventbus.Event) *value.Map {
	out := value.NewMap(3)
	out.Set("tag", e.Tag)
	out.Set("stamp", e.Stamp.UTC().Format(time.RFC3339Nano))
	if len(e.Data) > 0 {
		data := value.NewMap(len(e.Data))
		for _, k := range sortedKeys(e.Data) {
			data.Set(k, e.Data[k])
		}
		out.Set("data", data)
	}
	return out
}

// stepTimeout is how long a step waits.
//
// It comes from the context rather than from an argument. SPEC 19.1
// lists `timeout` among a step's options and SPEC 11.7 lists it among
// every state's, and they are the same option: the state runner strips
// it before the module is called and bounds the step's context with it.
// Reading it back here means one timeout rather than two that can
// disagree -- and they did, until this: a step written with
// `timeout: 10s` waited its ten seconds and then reported that the run
// had been stopped, because the module was waiting on its own default
// of five minutes while the context expired underneath it.
func stepTimeout(c *exec.Context) time.Duration {
	if deadline, ok := c.Ctx.Deadline(); ok {
		if left := time.Until(deadline); left > 0 {
			return left
		}
		return 0
	}
	return 5 * time.Minute
}

// tolerated reads the node globs a step may lose, under either name.
func tolerated(args *value.Map) []string {
	out := states.Strings(args, "tolerate_failures")
	// The old name, accepted so that an existing orchestration runs
	// unchanged. SPEC section 19.1.
	old := states.Strings(args, "fail_minions") // lexicon:allow
	return append(out, old...)
}
