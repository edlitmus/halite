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
			{Name: "batch_timeout", Type: signature.Duration,
				Doc: "How long each wave of a batched step waits for its returns before the next wave starts. " +
					"Defaults to the step's `timeout` when that is longer than five minutes, and to five minutes otherwise."},
			{Name: "batch_wait", Type: signature.Duration,
				Doc: "How long to pause between waves of a batched step."},
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

	// `run_parallel` is SPEC 9.6's `--parallel` for the job a step sends,
	// named apart from the per-state `parallel` option (SPEC 11.7, 19.1),
	// which is about running steps at once on the hub and is a different
	// question from how the job shares a node.
	//
	// The state steps declare it too, so that writing it on one is
	// answered by name rather than by "is not a parameter", and the
	// answer is a refusal at compile time (refuseParallelStateRuns): a
	// state run always runs alone on the node, so a step that asks for it
	// to share one has asked for something it will not get.
	runParallel := signature.Param{Name: "run_parallel", Type: signature.Bool,
		Doc: "Let the job run beside other jobs on each node, as `halite-hub run --parallel` " +
			"does (SPEC section 9.6). Ignored by the node on a signed job."}
	stateRunParallel := signature.Param{Name: "run_parallel", Type: signature.Bool,
		Doc: "Refused when true: a state run always runs alone on a node (SPEC section 9.6)."}

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
					stateRunParallel,
				),
			},
			Fn: o.fleetStep("salt.state"),
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
					stateRunParallel,
				),
			},
			Fn: o.fleetStep("salt.sls"),
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
					stateRunParallel,
				),
			},
			Fn: o.fleetStep("salt.highstate"),
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
					runParallel,
				),
			},
			Fn: o.fleetStep("salt.function"),
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

// fleetStep backs the four step functions that send a job: salt.state,
// salt.sls, salt.highstate and salt.function.
//
// Everything about which job a step sends is in stepJob, and this only
// dispatches it, because issue 765's plan tool and the hub's pre-flight of
// a plan have to work out the same job without running anything. Two
// copies of "what job does this step make" would be two things that must
// agree, and a plan fails closed when they do not.
func (o *orchRunner) fleetStep(step string) states.Func {
	return func(c *exec.Context, args *value.Map) (states.Result, error) {
		sub, honoursTest, refusal := stepJob(step, args, o.env, c.Test)
		if refusal != "" {
			return states.False(refusal), nil
		}
		return o.dispatchAndWait(c, args, sub, honoursTest)
	}
}

// stepJob is the job a fleet step sends, from its bound arguments.
//
// honoursTest says whether that job can be trusted to change nothing
// under test mode, which dispatchAndWait reads. A refusal is a reason the
// step sends nothing at all.
func stepJob(step string, args *value.Map, runEnv string, runTest bool) (sub Submission, honoursTest bool, refusal string) {
	switch step {
	case "salt.state", "salt.sls", "salt.highstate":
		sub, refusal = baseSubmission(args, "state.apply", runEnv)
		if refusal != "" {
			return Submission{}, false, refusal
		}
		if step != "salt.highstate" && !states.Bool(args, "highstate", false) {
			sub.Arg = states.Strings(args, "sls")
		}
		// A state honours test mode by contract -- SPEC 11.6, enforced for
		// every state module by the conformance harness -- so a test run
		// dispatches the job with `test` set and reports what each node
		// would do. That is the useful answer, and it is safe.
		sub.Test = sub.Test || runTest
		if p := states.Mapping(args, "pillar"); p != nil && p.Len() > 0 {
			sub.Kwarg = map[string]any{"pillar": p}
		}
		if env := states.Str(args, "pillarenv", ""); env != "" {
			if sub.Kwarg == nil {
				sub.Kwarg = map[string]any{}
			}
			sub.Kwarg["pillarenv"] = env
		}
		return sub, true, ""
	case "salt.function":
		fun := states.Str(args, "name", "")
		if fun == "" {
			return Submission{}, false, "A salt.function step needs `name`, the function to run."
		}
		sub, refusal = baseSubmission(args, fun, runEnv)
		if refusal != "" {
			return Submission{}, false, refusal
		}
		for _, a := range states.Strings(args, "arg") {
			sub.Arg = append(sub.Arg, a)
		}
		// Read here, not in baseSubmission: only this step may set it, and
		// the state steps run alone.
		sub.Parallel = states.Bool(args, "run_parallel", false)
		if kw := states.Mapping(args, "kwarg"); kw != nil && kw.Len() > 0 {
			sub.Kwarg = map[string]any{}
			for _, e := range kw.Entries() {
				sub.Kwarg[value.KeyString(e.Key)] = e.Val
			}
		}
		return sub, false, ""
	}
	return Submission{}, false, fmt.Sprintf("%s is not a step that sends a job", step)
}

// isFleetStep reports whether a step function sends a job to nodes.
func isFleetStep(step string) bool {
	switch step {
	case "salt.state", "salt.sls", "salt.highstate", "salt.function":
		return true
	}
	return false
}

// baseSubmission builds the common half of a fleet step, and reports the
// refusals that stop one before it is dispatched.
func baseSubmission(args *value.Map, fun, runEnv string) (Submission, string) {
	if raw, ok := args.Get("queue"); ok && raw != nil && value.Truthy(raw) {
		return Submission{}, "`queue` holds a step on the hub's durable queue, which is not built; " +
			"it arrives with the queue runner (SPEC section 19.4)."
	}
	env := states.Str(args, "saltenv", "")
	if env == "" {
		env = runEnv
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
		return Submission{}, "A fleet step needs `tgt`, the nodes to run against."
	}
	var refusal string
	if sub.Batch.Timeout, refusal = stepDuration(args, "batch_timeout"); refusal != "" {
		return Submission{}, refusal
	}
	if sub.Batch.Wait, refusal = stepDuration(args, "batch_wait"); refusal != "" {
		return Submission{}, refusal
	}
	return sub, ""
}

// stepDuration reads an optional duration argument of a fleet step. An
// unset one is zero, which is how a Batch says "use the default"; one that
// is set and cannot be read is a refusal, because a rolling step that asked
// for a pause and got none has been told the opposite of what it will do.
func stepDuration(args *value.Map, name string) (time.Duration, string) {
	raw, ok := args.Get(name)
	if !ok || raw == nil {
		return 0, ""
	}
	d, err := value.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Sprintf("`%s`: %v.", name, err)
	}
	if d < 0 {
		return 0, fmt.Sprintf("`%s` cannot be negative.", name)
	}
	return d, ""
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

	// A planned run sends the plan's signed job for the step. The policy
	// is still asked, in dispatchFor, as for any step: the signature is in
	// addition to authorisation, not instead of it. `run_parallel` stays
	// as the step set it and outside the signature, so a node that
	// requires signatures ignores it (SPEC 9.6, cmd/halite-node's
	// schedulingOf): a plan never authorises a job to run beside another.
	//
	// The plan is consulted before the step is recorded as dispatching: a
	// step its plan refuses sends nothing, and a record saying it might
	// have would make a resume past it refuse for a job that never went.
	// Signed is set first, so the pending record already says so.
	signed := o.plan != nil
	if signed {
		var err error
		if sub, err = o.signedStep(c.StateID, sub); err != nil {
			return states.False(err.Error()), nil
		}
		d := o.detail(c.StateID)
		o.mu.Lock()
		d.Signed = true
		o.mu.Unlock()
	}
	// An unsigned step's job lives as long as the step will wait for it,
	// and never less than the default (DIVERGENCE 5.735; SPEC 6.3 lets an
	// orchestration step extend it). Its job got the fifteen-minute
	// default whatever the step's `timeout`: a longer step still succeeded
	// on the node's late return while its job was settled partial and
	// counted expired, and a batched one stopped sending waves at minute
	// fifteen, because a node refuses a job past its expiry, and left the
	// rest of the estate untouched. A signed step's expiry is the plan's,
	// fixed when it was signed (orchplan), and is not changed here.
	timeout := stepTimeout(c)
	if !signed && timeout > job.DefaultTTL {
		sub.TTL = timeout
	}
	// A batched step's waves wait as long as the step does, when that is
	// longer than the default wave wait, and the step's own `batch_timeout`
	// wins over it (issue 872). Each wave waited DefaultBatchTimeout whatever
	// the step's `timeout`, so a `batch: 1` step whose function ran longer
	// than five minutes handed on to the next node while the first was still
	// working: a rolling deploy written to take one node out at a time took
	// out two. Set here and not in baseSubmission, as TTL is, because it
	// reads the step's deadline; a signed step's wave wait is not part of
	// its signature, which fixes the job and its expiry, and sliceWait still
	// stops a wave at that expiry.
	if sub.Batch.Timeout == 0 && timeout > DefaultBatchTimeout {
		sub.Batch.Timeout = timeout
	}
	o.dispatching(c.StateID)
	j, err := o.server.dispatchFor(o.principal, o.onBehalfOf, sub)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if signed {
		o.server.info("dispatched a signed orchestration step", "component", "orchestration",
			"jid", string(o.jid), "step", c.StateID, "job_jid", string(j.JID), "signed", true)
	}
	o.server.emit(tagOrchStep(string(o.jid), c.StateID), "", map[string]any{
		"jid": string(o.jid), "step": c.StateID, "job_jid": string(j.JID),
		"fun": sub.Fun, "target": sub.Target, "nodes": len(j.Nodes), "signed": signed,
	})
	o.dispatched(c.StateID, j)

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
		Principal:  o.principal,
		OnBehalfOf: o.onBehalfOf,
		Fun:        fun,
		Arg:        arg,
		Kwarg:      kwarg,
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

// OrchSignatures is the orchestration step set, for callers that need to
// know what a step may be named without running one.
//
// `halite migrate` judged every declaration in a tree against the
// node-side state registry, which does not hold these: an orchestration
// SLS using `salt.state` — correct, and what SPEC 19.1 keeps Salt's
// syntax for — was reported as a blocking gap against a step this build
// ships. The registry is built with no runner behind it because only the
// signatures are wanted; the step functions are never called.
func OrchSignatures() *signature.Registry {
	return orchStates(nil).Signatures()
}
