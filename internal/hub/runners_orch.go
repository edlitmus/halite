package hub

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// registerStateRunner installs the `state` runner of SPEC 19.2: the
// orchestration entry points.
func registerStateRunner(r *Runners) {
	orchParams := []signature.Param{
		runnerArg("sls", signature.String, "The orchestration SLS, or a comma list of them."),
		runnerOpt("env", signature.String, "", "The environment to compile from."),
		runnerOpt("pillar", signature.Map, nil, "Pillar overrides the run compiles with."),
		runnerOpt("test", signature.Bool, false, "Report what each step would do without dispatching."),
	}

	r.Add(
		RunnerModule{
			Sig: runnerSig("state", "orchestrate",
				"Compile an orchestration SLS on the hub and run it.", "19.1", orchParams...),
			Fn: runOrchestration,
		},
		RunnerModule{
			Sig: runnerSig("state", "orch",
				"Compile an orchestration SLS on the hub and run it. Salt's short name for "+
					"`state.orchestrate`.", "19.1", orchParams...),
			Fn: runOrchestration,
		},
		RunnerModule{
			Sig: runnerSig("state", "orchestrate_show_sls",
				"Compile an orchestration and print the steps it would run, in order, "+
					"without running any.", "19.1",
				runnerArg("sls", signature.String, "The orchestration SLS, or a comma list of them."),
				runnerOpt("env", signature.String, "", "The environment to compile from."),
				runnerOpt("pillar", signature.Map, nil, "Pillar overrides the run compiles with."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				req := orchRequestFrom(c)
				env := req.Env
				if env == "" {
					env = "base"
				}
				if c.Server.Files == nil {
					return nil, fmt.Errorf("this hub serves no tree, so it has no orchestration to compile")
				}
				orch := &orchRunner{server: c.Server, principal: c.Principal, env: env}
				registry := orchStates(orch)
				compiled := c.Server.compileOrchestration(req, env, "", registry)
				if err := compiled.Err(); err != nil {
					return nil, err
				}
				steps := make([]any, 0, len(compiled.Low))
				for _, ch := range compiled.Low {
					step := value.NewMap(4)
					step.Set("id", ch.ID)
					step.Set("fun", ch.Func())
					step.Set("sls", ch.SLS)
					step.Set("order", int64(ch.RunNum))
					if tgt, ok := ch.Args.Get("tgt"); ok {
						step.Set("tgt", tgt)
					}
					steps = append(steps, step)
				}
				out := value.NewMap(2)
				out.Set("sls", stringList(compiled.SLS))
				out.Set("steps", steps)
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("state", "orch_show",
				"The timeline of one orchestration run: every step, in the order it ran, "+
					"with its result and the nodes it reached.", "19.1",
				runnerArg("jid", signature.String, "The orchestration's job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				id, err := parseJID(c.arg("jid"))
				if err != nil {
					return nil, err
				}
				run, err := c.Server.orchStore().Get(id)
				if err != nil {
					return nil, err
				}
				return orchJSON(run, true), nil
			},
		},
		RunnerModule{
			Sig: runnerSig("state", "orch_list",
				"Recent orchestration runs, newest first.", "19.1",
				runnerOpt("limit", signature.Int, 20, "How many to list."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				runs, err := c.Server.orchStore().List(c.argInt("limit"))
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(runs))
				for _, run := range runs {
					out.Set(string(run.JID), orchJSON(run, false))
				}
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("state", "orch_resume",
				"Run an orchestration again from a named step, carrying the steps before "+
					"it forward as they finished. Salt cannot do this, and it is what makes "+
					"a long deployment orchestration usable after one step fails.", "19.1",
				runnerArg("jid", signature.String, "The run to pick up."),
				runnerArg("from", signature.String, "The step to start at."),
				runnerOpt("test", signature.Bool, false, "Report what each step would do without dispatching."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				id, err := parseJID(c.arg("jid"))
				if err != nil {
					return nil, err
				}
				previous, err := c.Server.orchStore().Get(id)
				if err != nil {
					return nil, err
				}
				run, err := c.Server.Orchestrate(c.Ctx, OrchRequest{
					Principal:  c.Principal,
					SLS:        previous.SLS,
					Env:        previous.Env,
					Test:       c.argBool("test"),
					ResumeOf:   id,
					ResumeFrom: c.arg("from"),
				})
				if err != nil {
					return nil, err
				}
				return orchOutcome(run)
			},
		},
		RunnerModule{
			Sig: runnerSig("state", "single",
				"Run one state against the hub itself.", "19.2"),
			Pending: "the phase that gives the hub a state surface of its own; SPEC 25.5 " +
				"keeps the modules that would need off it (SPEC section 19.2)",
		},
		RunnerModule{
			Sig: runnerSig("state", "event",
				"Fire an event from inside an orchestration.", "19.2"),
			Pending: "`event.send` fires an event today; firing one from inside a step does not (SPEC section 19.2)",
		},
		RunnerModule{
			Sig: runnerSig("state", "pause",
				"Hold a running orchestration at its next step.", "19.2"),
			Pending: "it needs the durable work queue of SPEC section 19.4",
		},
		RunnerModule{
			Sig: runnerSig("state", "resume",
				"Let a held orchestration continue.", "19.2"),
			Pending: "it needs the durable work queue of SPEC section 19.4",
		},
	)
}

// runOrchestration backs state.orchestrate and its short name.
func runOrchestration(c *RunnerContext) (any, error) {
	run, err := c.Server.Orchestrate(c.Ctx, orchRequestFrom(c))
	if err != nil {
		return nil, err
	}
	return orchOutcome(run)
}

// orchOutcome is the shape every orchestration entry point returns.
//
// The run happened and it failed is a result rather than an error, but
// the caller's exit status has to reflect it — so the runner reports
// failure *and* carries the timeline, which is what the operator's next
// command needs in order to name a step to resume from.
func orchOutcome(run *OrchRun) (any, error) {
	out := orchJSON(run, true)
	if run.State == OrchFailed {
		return out, fmt.Errorf("orchestration %s failed; `orch show %s` has the timeline",
			run.JID, run.JID)
	}
	return out, nil
}

// orchRequestFrom reads the arguments common to the orchestration
// entry points.
func orchRequestFrom(c *RunnerContext) OrchRequest {
	req := OrchRequest{
		Principal: c.Principal,
		SLS:       tagGlobs(c.arg("sls")),
		Env:       c.arg("env"),
		Test:      c.argBool("test"),
	}
	if raw, ok := c.Args.Get("pillar"); ok && raw != nil {
		if m, ok := raw.(*value.Map); ok {
			req.Pillar = m
		}
	}
	return req
}

// orchJSON renders a run. The steps are included for one run and left
// out of a listing, where the point is which runs there were.
func orchJSON(run *OrchRun, withSteps bool) *value.Map {
	out := value.NewMap(8)
	out.Set("jid", string(run.JID))
	out.Set("sls", stringList(run.SLS))
	if run.Env != "" {
		out.Set("env", run.Env)
	}
	if run.Principal != "" {
		out.Set("principal", run.Principal)
	}
	out.Set("started", run.Started.UTC().Format(time.RFC3339Nano))
	out.Set("duration_ms", run.DurationMS)
	out.Set("state", run.State)
	if run.ResumedFrom != "" {
		out.Set("resumed_from", run.ResumedFrom)
		out.Set("resumed_of", string(run.ResumedOf))
	}
	if !withSteps {
		out.Set("steps", int64(len(run.Steps)))
		return out
	}

	steps := make([]any, 0, len(run.Steps))
	for _, step := range run.Steps {
		steps = append(steps, orchStepJSON(step))
	}
	out.Set("steps", steps)
	return out
}

func orchStepJSON(step *OrchStep) *value.Map {
	out := value.NewMap(10)
	out.Set("id", step.ID)
	out.Set("fun", step.Fun)
	out.Set("order", int64(step.Order))
	out.Set("result", step.Succeeded())
	if step.Skipped {
		out.Set("skipped", true)
	}
	if step.Comment != "" {
		out.Set("comment", step.Comment)
	}
	out.Set("duration_ms", step.DurationMS)
	if step.JID != "" {
		out.Set("job_jid", step.JID)
	}
	if len(step.Nodes) > 0 {
		out.Set("nodes", stringList(step.Nodes))
	}
	if len(step.Failed) > 0 {
		out.Set("failed", stringList(step.Failed))
	}
	if len(step.Missing) > 0 {
		out.Set("missing", stringList(step.Missing))
	}
	if len(step.Returns) > 0 {
		rets := value.NewMap(len(step.Returns))
		for _, node := range sortedReturnKeys(step.Returns) {
			decoded, err := value.DecodeJSON(step.Returns[node])
			if err != nil {
				decoded = string(step.Returns[node])
			}
			rets.Set(node, decoded)
		}
		out.Set("returns", rets)
	}
	return out
}

func sortedReturnKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
