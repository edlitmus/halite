package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/job"
)

var jobsUsage = `halite-hub jobs — the job cache (SPEC section 9.4)

  jobs list [--limit <n>]      recent jobs, newest first
  jobs show <jid>              one job and every return filed against it
  jobs lookup <jid>            the same, as JSON
  jobs missing <jid>           the nodes that were sent it and have not answered
  jobs active                  jobs the hub has not finished delivering
  jobs resume <jid>            pick up a batch the hub was part way through
  jobs kill <jid>              stop a job that has not finished
  jobs export <jid>            the job and its returns as one JSON document
  jobs prune                   enforce retention now rather than on the hour

jobs flags:
  --limit <n>          how many jobs to list, default 20
  --out <format>       summary (default) or json
`

// openJobs reads the cache directly.
//
// `jobs` is a hub-side record and this command runs on the hub, so it
// reads the files rather than asking the running process: the answer is
// the same, and it works when the hub is stopped, which is exactly when
// an operator most wants to know what happened.
func openJobs(args *cli.Args) *job.Cache {
	cfg, err := config.Load(config.Hub, config.LoadOptions{
		Path:         args.Flag("config", ""),
		Root:         args.Flag("root", config.DefaultRoot),
		AllowMissing: true,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	dir := cfg.String("state_dir", config.DefaultStateDir) + "/jobs"
	cache, err := job.OpenCache(dir)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	cache.Retention = cfg.Duration("job_cache_retention", 30*24*time.Hour)
	cache.MaxBytes = cfg.Int("job_cache_max_size", 10<<30)
	return cache
}

func runJobs(args *cli.Args) int {
	if len(args.Positional) == 0 {
		fmt.Fprint(os.Stderr, jobsUsage)
		return 2
	}
	if args.Positional[0] == "help" {
		fmt.Print(jobsUsage)
		return 0
	}
	cache := openJobs(args)
	rest := args.Positional[1:]

	switch args.Positional[0] {
	case "list":
		limit := 20
		fmt.Sscanf(args.Flag("limit", "20"), "%d", &limit)
		jobs, err := cache.List(limit)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		if args.Flag("out", "summary") != "summary" {
			return writeOut(args, jobs)
		}
		if len(jobs) == 0 {
			fmt.Println("no jobs")
			return 0
		}
		for _, j := range jobs {
			missing, _ := cache.Missing(j.JID)
			state := string(j.State)
			if len(missing) > 0 && state != string(job.Complete) {
				state = fmt.Sprintf("%s, %d outstanding", state, len(missing))
			}
			if j.Batch.Size > 0 {
				state = fmt.Sprintf("%s, %d/%d delivered", state, len(j.Delivered), len(j.Nodes))
			}
			fmt.Printf("%s  %-24s %-14s %s\n", j.JID, j.Fun, j.Target, state)
		}
		return 0

	case "show", "lookup":
		if len(rest) == 0 {
			cli.Fatalf("%s needs a jid", args.Positional[0])
		}
		id := job.ID(rest[0])
		j, err := cache.Get(id)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		returns, err := cache.Returns(id)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		if args.Positional[0] == "lookup" || args.Flag("out", "summary") != "summary" {
			return writeOut(args, map[string]any{"job": j, "returns": returns})
		}
		fmt.Printf("jid       %s\n", j.JID)
		fmt.Printf("function  %s %s\n", j.Fun, strings.Join(j.Arg, " "))
		fmt.Printf("target    %s (%s)\n", j.Target, j.TargetKind)
		fmt.Printf("submitter %s\n", j.Submitter)
		fmt.Printf("created   %s\n", j.Created.UTC().Format(time.RFC3339))
		fmt.Printf("expires   %s\n", j.Expires.UTC().Format(time.RFC3339))
		fmt.Printf("nodes     %s\n", strings.Join(j.Nodes, ", "))
		if j.Batch.Size > 0 {
			fmt.Printf("batch     %d at a time, %d delivered\n", j.Batch.Size, len(j.Delivered))
			if remaining := j.Remaining(); len(remaining) > 0 {
				fmt.Printf("to go     %s\n", strings.Join(remaining, ", "))
			}
		}
		missing, _ := cache.Missing(id)
		if len(missing) > 0 {
			fmt.Printf("missing   %s\n", strings.Join(missing, ", "))
		}
		fmt.Println()
		for _, r := range returns {
			outcome := "ok"
			if !r.Success {
				outcome = "FAILED"
			}
			fmt.Printf("%s: %s (retcode %d, %d ms)\n", r.NodeID, outcome, r.RetCode, r.DurationMS)
			printReturn(r)
		}
		return 0

	case "missing":
		if len(rest) == 0 {
			cli.Fatalf("missing needs a jid")
		}
		nodes, err := cache.Missing(job.ID(rest[0]))
		if err != nil {
			cli.Fatalf("%v", err)
		}
		for _, id := range nodes {
			fmt.Println(id)
		}
		if len(nodes) > 0 {
			return 1
		}
		return 0

	case "active":
		jobs, err := cache.List(200)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		found := 0
		now := time.Now()
		for _, j := range jobs {
			if j.State != job.Batching && j.State != job.Dispatched {
				continue
			}
			// A job past its expiry is not in flight, whatever its
			// record still says: the hub settles those on a timer, and
			// this list should not wait for the timer to be honest.
			if j.Expired(now) {
				continue
			}
			missing, _ := cache.Missing(j.JID)
			if j.State == job.Dispatched && len(missing) == 0 {
				continue
			}
			found++
			fmt.Printf("%s  %-20s %-12s delivered %d/%d, %d outstanding\n",
				j.JID, j.Fun, j.State, len(j.Delivered), len(j.Nodes), len(missing))
		}
		if found == 0 {
			fmt.Println("nothing is in flight")
		}
		return 0

	case "resume":
		if len(rest) == 0 {
			cli.Fatalf("resume needs a jid; `jobs active` lists what is in flight")
		}
		// Resuming asks the running hub, because the hub is what owns
		// a batch. Reading the cache would say what is left and could
		// not deliver any of it.
		client := operatorClient(args)
		res, err := client.ResumeJob(context.Background(), rest[0])
		if err != nil {
			cli.Fatalf("%v", err)
		}
		if len(res.Remaining) == 0 {
			fmt.Printf("%s has already reached every node\n", res.JID)
			return 0
		}
		fmt.Printf("resuming %s: %d node(s) to go\n", res.JID, len(res.Remaining))
		return 0

	case "kill":
		if len(rest) == 0 {
			cli.Fatalf("kill needs a jid")
		}
		client := operatorClient(args)
		res, err := client.KillJob(context.Background(), rest[0])
		if err != nil {
			cli.Fatalf("%v", err)
		}
		// What can be stopped is what has not happened yet. A node
		// already applying a state is not interrupted, and saying so
		// is better than implying otherwise.
		fmt.Printf("killed %s\n", res.JID)
		if len(res.Told) > 0 {
			fmt.Printf("  told: %s\n", strings.Join(res.Told, ", "))
		}
		if len(res.Unqueued) > 0 {
			fmt.Printf("  never sent: %s\n", strings.Join(res.Unqueued, ", "))
		}
		fmt.Println("  a node already running the job finishes what it started")
		return 0

	case "export":
		if len(rest) == 0 {
			cli.Fatalf("export needs a jid")
		}
		id := job.ID(rest[0])
		j, err := cache.Get(id)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		returns, err := cache.Returns(id)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		missing, _ := cache.Missing(id)
		// One document, so that an incident record is one file: the
		// job as submitted, every return, and who never answered.
		out, err := json.MarshalIndent(map[string]any{
			"job": j, "returns": returns, "missing": missing,
		}, "", "  ")
		if err != nil {
			cli.Fatalf("%v", err)
		}
		fmt.Println(string(out))
		return 0

	case "prune":
		removed, err := cache.Prune()
		if err != nil {
			cli.Fatalf("%v", err)
		}
		fmt.Printf("pruned %d job(s)\n", removed)
		return 0
	}
	fmt.Fprintf(os.Stderr, "halite-hub jobs: unknown subcommand %q\n\n%s", args.Positional[0], jobsUsage)
	return 2
}
