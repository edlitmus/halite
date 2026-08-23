package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// runOrch is `halite-hub orch <run|show|list|resume|lint>`, the old
// `salt-run state.orchestrate` and the timeline Salt has no equivalent
// for.
//
// Every subcommand is a runner call, because an orchestration runs on
// the hub and this program is the operator's side of it. That also
// means an orchestration is authorized exactly as any other hub
// function: by the caller's `runners:` grant, and again as each job a
// step dispatches.
func runOrch(args *cli.Args) int {
	if len(args.Positional) == 0 {
		fmt.Fprint(os.Stderr, orchUsage)
		return 2
	}

	switch args.Positional[0] {
	case "run":
		return orchRun(args, false)
	case "lint", "show-sls":
		return orchRun(args, true)
	case "show":
		return orchShow(args)
	case "list":
		return orchList(args)
	case "resume":
		return orchResume(args)
	}
	cli.Fatalf("orch has no subcommand %q; there are run, show, list, resume, and lint", args.Positional[0])
	return 2
}

func orchRun(args *cli.Args, lintOnly bool) int {
	if len(args.Positional) < 2 {
		cli.Fatalf("orch %s needs an orchestration SLS", args.Positional[0])
	}
	kwargs := map[string]any{
		"sls": strings.Join(args.Positional[1:], ","),
	}
	if env := args.Flag("env", ""); env != "" {
		kwargs["env"] = env
	}
	fun := "state.orchestrate"
	if lintOnly {
		fun = "state.orchestrate_show_sls"
	} else if args.Bool("test", false) {
		kwargs["test"] = true
	}
	if raw := args.Flag("pillar", ""); raw != "" {
		decoded, err := value.DecodeJSON([]byte(raw))
		if err != nil {
			cli.Fatalf("--pillar %q is not JSON: %v", raw, err)
		}
		kwargs["pillar"] = decoded
	}
	return orchCall(args, fun, nil, kwargs)
}

func orchShow(args *cli.Args) int {
	if len(args.Positional) < 2 {
		cli.Fatalf("orch show needs a jid")
	}
	return orchCall(args, "state.orch_show", args.Positional[1:2], nil)
}

func orchList(args *cli.Args) int {
	kwargs := map[string]any{}
	if limit := args.Flag("limit", ""); limit != "" {
		kwargs["limit"] = limit
	}
	return orchCall(args, "state.orch_list", nil, kwargs)
}

func orchResume(args *cli.Args) int {
	if len(args.Positional) < 2 {
		cli.Fatalf("orch resume needs a jid")
	}
	from := args.Flag("from", "")
	if from == "" {
		cli.Fatalf("orch resume needs --from <step>; `orch show %s` lists the steps", args.Positional[1])
	}
	kwargs := map[string]any{"from": from}
	if args.Bool("test", false) {
		kwargs["test"] = true
	}
	return orchCall(args, "state.orch_resume", args.Positional[1:2], kwargs)
}

// orchCall makes the runner call and renders the answer.
func orchCall(args *cli.Args, fun string, arg []string, kwargs map[string]any) int {
	client := operatorClient(args)
	res, err := client.Runner(context.Background(), transport.RunnerRequest{
		Fun: fun, Arg: arg, Kwarg: kwargs,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}

	decoded, decodeErr := value.DecodeJSON(res.Return)
	format, err := cli.ParseFormat(args.Flag("out", "timeline"))
	if args.Flag("out", "timeline") == "timeline" {
		// A failed run still has a timeline, and printing it is the
		// point: the operator's next command is `orch resume --from`,
		// and they need the step name to pass it.
		if decodeErr == nil {
			printTimeline(decoded)
		}
		if !res.Success {
			fmt.Fprintf(os.Stderr, "%s\n", res.Error)
			return 1
		}
		return 0
	}
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if decodeErr != nil {
		cli.Fatalf("the hub's answer is not readable: %v", decodeErr)
	}
	if err := cli.Write(os.Stdout, decoded, format, 2); err != nil {
		cli.Fatalf("%v", err)
	}
	if !res.Success {
		fmt.Fprintf(os.Stderr, "%s\n", res.Error)
		return 1
	}
	return 0
}

// printTimeline renders a run the way an operator reads one: a line per
// step, in the order they ran, with what each reached.
func printTimeline(v any) {
	if v == nil {
		return
	}
	run, ok := v.(*value.Map)
	if !ok {
		fmt.Println(v)
		return
	}
	// A listing is a mapping of jid to run rather than one run.
	if !run.Has("jid") {
		for _, e := range run.Entries() {
			if entry, ok := e.Val.(*value.Map); ok {
				printRunLine(entry)
			}
		}
		return
	}
	steps, hasSteps := run.Get("steps")
	if !hasSteps {
		printRunLine(run)
		return
	}
	list, ok := steps.([]any)
	if !ok {
		// `orch list` puts a count here rather than the steps.
		printRunLine(run)
		return
	}

	if jid, _ := run.Get("jid"); jid != nil {
		state, _ := run.Get("state")
		fmt.Printf("%v  %v\n", jid, state)
	}
	if from, ok := run.Get("resumed_from"); ok {
		of, _ := run.Get("resumed_of")
		fmt.Printf("resumed %v from step %v\n", of, from)
	}
	for _, item := range list {
		step, ok := item.(*value.Map)
		if !ok {
			continue
		}
		printStepLine(step)
	}
}

func printRunLine(run *value.Map) {
	jid, _ := run.Get("jid")
	state, _ := run.Get("state")
	sls, _ := run.Get("sls")
	steps, _ := run.Get("steps")
	fmt.Printf("%-24v %-9v %v step(s)  %v\n", jid, state, steps, renderList(sls))
}

func printStepLine(step *value.Map) {
	mark := "ok  "
	if ok, _ := step.Get("result"); ok == false {
		mark = "FAIL"
	}
	if skipped, _ := step.Get("skipped"); skipped == true {
		mark = "skip"
	}
	id, _ := step.Get("id")
	fun, _ := step.Get("fun")
	fmt.Printf("  %s %-28v %-20v", mark, id, fun)
	if jid, ok := step.Get("job_jid"); ok {
		fmt.Printf(" job %v", jid)
	}
	if nodes, ok := step.Get("nodes"); ok {
		if list, ok := nodes.([]any); ok {
			fmt.Printf(" on %d node(s)", len(list))
		}
	}
	fmt.Println()
	if comment, ok := step.Get("comment"); ok && comment != "" {
		fmt.Printf("       %v\n", comment)
	}
	for _, key := range []string{"failed", "missing"} {
		if v, ok := step.Get(key); ok {
			fmt.Printf("       %s: %s\n", key, renderList(v))
		}
	}
}

func renderList(v any) string {
	list, ok := v.([]any)
	if !ok {
		return fmt.Sprint(v)
	}
	parts := make([]string, len(list))
	for i, item := range list {
		parts[i] = fmt.Sprint(item)
	}
	return strings.Join(parts, ", ")
}

var orchUsage = `halite-hub orch — orchestration, which runs on the hub

Usage:
  halite-hub orch run <sls>...            compile and run an orchestration
  halite-hub orch lint <sls>...           print the steps without running any
  halite-hub orch show <jid>              the timeline of one run
  halite-hub orch list                    recent runs, newest first
  halite-hub orch resume <jid> --from <step>
                                          run again from a named step

Examples:
  halite-hub orch run deploy.web
  halite-hub orch run deploy.web --pillar '{"version":"1.2"}'
  halite-hub orch resume 20260823T101500.000000 --from deploy_web

A step is authorized twice: once as the orchestration, and again as the
job it dispatches. Permission to run an orchestration is not permission
to run anything it happens to name.
`
