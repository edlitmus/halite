package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
		return cli.ExitUsage
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
	case "plan":
		return orchPlan(args)
	}
	// Not cli.Fatalf, which this was: that exits 1, so the `return 2`
	// that followed it never ran and a mistyped subcommand reported a
	// failed run. A typo is a usage error, as it is for every other
	// dispatcher in this program.
	fmt.Fprintf(os.Stderr, "halite-hub orch has no subcommand %q; there are run, show, list, resume, lint, and plan\n",
		args.Positional[0])
	return cli.ExitUsage
}

func orchRun(args *cli.Args, lintOnly bool) int {
	if len(args.Positional) < 2 {
		cli.Usagef("orch %s needs an orchestration SLS", args.Positional[0])
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
			cli.Usagef("--pillar %q is not JSON: %v", raw, err)
		}
		kwargs["pillar"] = decoded
	}
	if lintOnly {
		return orchCall(args, fun, nil, kwargs)
	}
	planArgument(args, kwargs)
	return orchStartAndWait(args, fun, nil, kwargs)
}

func orchShow(args *cli.Args) int {
	if len(args.Positional) < 2 {
		cli.Usagef("orch show needs a jid")
	}
	return orchCall(args, "state.orch_show", args.Positional[1:2], nil)
}

func orchList(args *cli.Args) int {
	kwargs := map[string]any{}
	// A whole number, and sent as one. It went as the string it was
	// typed as, which the hub read with Sscanf: `--limit abc` was 0 and
	// listed the default, `--limit 5x` was 5, and `--limit=-1` was
	// handed on. DIVERGENCE 5.608.
	if limit := cli.IntFlag(args, "limit", 0, 1); limit > 0 {
		kwargs["limit"] = limit
	}
	return orchCall(args, "state.orch_list", nil, kwargs)
}

func orchResume(args *cli.Args) int {
	if len(args.Positional) < 2 {
		cli.Usagef("orch resume needs a jid")
	}
	from := args.Flag("from", "")
	if from == "" {
		cli.Usagef("orch resume needs --from <step>; `orch show %s` lists the steps", args.Positional[1])
	}
	kwargs := map[string]any{"from": from}
	if args.Bool("test", false) {
		kwargs["test"] = true
	}
	// The override the run was started with; the hub refuses a resume
	// whose override differs from it (DIVERGENCE 5.201).
	if raw := args.Flag("pillar", ""); raw != "" {
		decoded, err := value.DecodeJSON([]byte(raw))
		if err != nil {
			cli.Usagef("--pillar %q is not JSON: %v", raw, err)
		}
		kwargs["pillar"] = decoded
	}
	planArgument(args, kwargs)
	return orchStartAndWait(args, "state.orch_resume", args.Positional[1:2], kwargs)
}

// orchCall makes the runner call and renders the answer.
//
// --out is read before the call. It was read after, so `orch run
// deploy.web --out tabel` ran the orchestration and then refused the
// format, as did a resume, and an operator who corrected the typo ran
// it a second time. DIVERGENCE 5.608.
func orchCall(args *cli.Args, fun string, arg []string, kwargs map[string]any) int {
	timeline, format, err := cli.ParseOut("timeline", args.Flag("out", "timeline"))
	if err != nil {
		cli.Usagef("%v", err)
	}
	client := operatorClient(args)
	res, err := client.Runner(context.Background(), transport.RunnerRequest{
		Fun: fun, Arg: arg, Kwarg: kwargs,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	return renderOrch(res.Return, res.Success, res.Error, timeline, format)
}

// Exit statuses of a waited-for run besides 0 and 1. 3 is `run`'s for a
// node that never answered: the outcome is not known, which is neither
// success nor failure.
const exitOutcomeUnseen = 3

// orchPollInterval is how often a waited-for run's job is read. A read
// writes nothing on the hub -- not a job, not an event -- so this is
// cheap for a run of hours; a second is how late the outcome may be
// printed.
var orchPollInterval = time.Second

// orchStartAndWait is `orch run` and `orch resume`: start the run, name
// it, and wait for its outcome a request at a time.
//
// They waited on the one request that started the run, and an operator
// request has thirty seconds (operatorTimeout). A run longer than that --
// a database initialisation, a data migration -- failed here with
// "context deadline exceeded" and exit 1 while it carried on on the hub,
// and its outcome went to nobody (issue 815). Raising the timeout would
// only move that line, and still leave an operator whose terminal dropped
// with no jid to look at.
//
// So the run is started `async`: the hub answers once the run is recorded,
// with its jid, which is printed before anything is waited for. The
// outcome is the answer to the runner call's own job, filed when the run
// ends, and it is read as `run` reads a node's job -- polled, each poll an
// ordinary short request. A run refused before its first step is still
// the first call's answer and is printed as it always was.
//
// What this cannot see is said rather than guessed, and exits
// exitOutcomeUnseen: an interrupt (the run is still running), a hub that
// restarted (the run stopped with it), and a hub that stops answering (it
// may be either). Each names the command to look with. Exit 1 stays what
// it was, a run that failed.
func orchStartAndWait(args *cli.Args, fun string, arg []string, kwargs map[string]any) int {
	timeline, format, err := cli.ParseOut("timeline", args.Flag("out", "timeline"))
	if err != nil {
		cli.Usagef("%v", err)
	}
	kwargs["async"] = true
	client := operatorClient(args)
	res, err := client.Runner(context.Background(), transport.RunnerRequest{
		Fun: fun, Arg: arg, Kwarg: kwargs,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	jid := transport.StartedRun(res)
	if jid == "" {
		return renderOrch(res.Return, res.Success, res.Error, timeline, format)
	}
	ret, unseen := waitForStartedRun(client, res.JID, jid)
	if ret == nil {
		return unseen
	}
	message := ""
	if !ret.Success {
		// The cached return of a run that failed is its timeline, and the
		// hub's sentence about it is not kept beside it; a run that
		// never produced one -- a panic -- is cached as its error text.
		if text := ret.FailureText(); text != "" {
			return renderOrch(nil, false, text, timeline, format)
		}
		message = failedRunMessage(jid)
	}
	return renderOrch(ret.Return, ret.Success, message, timeline, format)
}

// failedRunMessage is what is said of a waited-for run that failed.
func failedRunMessage(jid string) string {
	return fmt.Sprintf("orchestration %s failed; `orch show %s` has the timeline", jid, jid)
}

// waitForStartedRun says that a run has started and how to look at it,
// and waits for the outcome of the runner call that started it. It is
// `orch run`'s wait and `runner state.orchestrate`'s alike (issues 815,
// 822).
//
// A wait that does not see the outcome says why and gives a nil return
// and exitOutcomeUnseen.
func waitForStartedRun(client *transport.Client, callJID, jid string) (*transport.RunnerReturn, int) {
	fmt.Fprintf(os.Stderr, "orchestration %s is running on the hub; stopping this command does not stop it, "+
		"and `halite-hub orch show %s` has its timeline\n", jid, jid)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ret, err := client.AwaitRunnerReturn(ctx, callJID, orchPollInterval, orchGiveUpAfter)
	if errors.Is(err, transport.ErrStoppedWaiting) {
		// Stopping this command sends the hub nothing, and the run lives
		// in the hub's process, not in this request: it was running at the
		// last poll, and this command did not change that.
		fmt.Fprintf(os.Stderr, "\nstopped waiting; orchestration %s is still running on the hub, and "+
			"`halite-hub orch show %s` has its timeline\n", jid, jid)
		return nil, exitOutcomeUnseen
	}
	if errors.Is(err, transport.ErrRunnerAborted) {
		fmt.Fprintf(os.Stderr, "the hub stopped while orchestration %s was running, and the run stopped with it; "+
			"`halite-hub orch show %s` has how far it got, and `orch resume %s --from <step>` picks it up\n",
			jid, jid, jid)
		return nil, exitOutcomeUnseen
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\norchestration %s may still be running on the hub; "+
			"`halite-hub orch show %s` has its timeline\n", err, jid, jid)
		return nil, exitOutcomeUnseen
	}
	return ret, 0
}

// orchGiveUpAfter is how long the hub may fail to answer, poll after
// poll, before the wait is abandoned. A restart of the hub stops the run
// too, so the outcome would never come; a network blip should not end a
// wait of hours.
var orchGiveUpAfter = time.Minute

// renderOrch prints a run, or a listing, the way --out asks, and gives
// the exit status its success says.
func renderOrch(raw []byte, success bool, message string, timeline bool, format cli.Format) int {
	decoded, decodeErr := value.DecodeJSON(raw)
	if raw == nil {
		decoded, decodeErr = nil, nil
	}
	if timeline {
		// A failed run still has a timeline, and printing it is the
		// point: the operator's next command is `orch resume --from`,
		// and they need the step name to pass it.
		if decodeErr == nil {
			printTimeline(decoded)
		}
		if !success {
			fmt.Fprintf(os.Stderr, "%s\n", message)
			return 1
		}
		return 0
	}
	if decodeErr != nil {
		cli.Fatalf("the hub's answer is not readable: %v", decodeErr)
	}
	if err := cli.Write(os.Stdout, decoded, format, 2); err != nil {
		cli.Fatalf("%v", err)
	}
	if !success {
		fmt.Fprintf(os.Stderr, "%s\n", message)
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
	// Dispatched with no outcome yet: not a success, which "ok" would
	// claim of a step whose result is still null.
	if pending, _ := step.Get("pending"); pending == true {
		mark = "wait"
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
  halite-hub orch plan <sls>... --tree <dir> --out <file> (--sign-key <path> | --sign-extension <path>)
                                          compile and sign a plan off the hub, for nodes
                                          that require signed jobs (SPEC section 25.6)
  halite-hub orch plan show <file>        print every job a plan signs

Examples:
  halite-hub orch run deploy.web
  halite-hub orch run deploy.web --pillar '{"version":"1.2"}'
  halite-hub orch resume 20260823T101500.000000 --from deploy_web
  halite-hub orch plan deploy.web --tree ~/estate/state --pillar '{"version":"1.2"}' \
      --sign-key ops.key --out deploy.plan.json
  halite-hub orch run deploy.web --pillar '{"version":"1.2"}' --plan deploy.plan.json

run and resume print the run's jid as soon as it has started, then wait for its outcome
for as long as the run takes. Stopping the command does not stop the run: it exits 3, and
` + "`orch show <jid>`" + ` has the run.

Flags for run and resume:
  --plan <file>        send every fleet step as the plan's signed job; the hub refuses
                       the whole run before any step if its compile disagrees with the plan

Flags for plan:
  --tree <dir>         the checkout to compile from; the hub is not contacted
  --out <file>         the plan file to write
  --sign-key <path>    a PEM key, as keys signer create writes
  --sign-extension <path>  a bridged signer extension, such as halite-ext-signer-kms
  --env <name>         the environment, as orch run --env; default base
  --pillar <json>      the override, which orch run must be given too
  --test               a test plan, for orch run --test
  --ttl <dur>          the window for the whole run, every batch and wait included;
                       default 1h
  --max-ttl <dur>      the nodes' job_signature_max_ttl; default 1h. A --ttl over it is
                       refused: split a long orchestration into successive plans, or raise
                       the setting on those nodes, which lengthens every signed job's life
  --resume-of <jid> --from <step>
                       a resume plan, signing <step> and every step after it; a resume
                       always needs a new plan

A plan authorises a set of jobs, not their order: make dependent actions one step.
A planned orchestration may not read the hub's configuration (opts).

A step is authorized twice: once as the orchestration, and again as the
job it dispatches. Permission to run an orchestration is not permission
to run anything it happens to name.
`
