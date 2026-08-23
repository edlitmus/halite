package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/eventbus"
)

var eventUsage = `halite-hub event — the event bus (SPEC section 17)

  event listen [--tag <glob>]...   stream the bus as NDJSON
  event tags                       the tag namespace this build fires

event flags:
  --tag <glob>         a tag to follow, repeatable as a comma list;
                       default everything. ** matches any depth.
  --from <position>    latest (default), earliest, or an offset
  --limit <n>          how many past records to read at a time, default 200
  --once               read what is there and stop, rather than following
`

func runEvent(args *cli.Args) int {
	if len(args.Positional) == 0 || args.Positional[0] == "help" {
		fmt.Print(eventUsage)
		return 2
	}
	switch args.Positional[0] {
	case "tags":
		// The namespace, so that a reactor author does not have to
		// read the specification to find out what fires.
		for _, row := range [][2]string{
			{"halite/job/<jid>/new", "a job is published"},
			{"halite/job/<jid>/ret/<node>", "a return arrives"},
			{"halite/state/<jid>/<node>/<result>", "a state run completes"},
			{"halite/node/<node>/start", "a node connects"},
			{"halite/node/<node>/stop", "a node disconnects"},
			{"halite/node/<node>/enroll/<state>", "an enrollment changes state"},
			{"halite/node/<node>/...", "anything a node sends with `halite-node event send`"},
			{"halite/key/<node>/<action>", "a key lifecycle action"},
			{"halite/presence/change", "the connected set changes"},
			{"halite/error/...", "a structured error worth reacting to"},
		} {
			fmt.Printf("%-38s %s\n", row[0], row[1])
		}
		return 0

	case "listen":
		client := operatorClient(args)
		var tags []string
		if v := args.Flag("tag", ""); v != "" && v != "true" {
			tags = strings.Split(v, ",")
		}
		limit := 200
		fmt.Sscanf(args.Flag("limit", "200"), "%d", &limit)
		from := args.Flag("from", eventbus.Latest)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		err := client.FollowEvents(ctx, tags, from, !args.Bool("once", false), limit,
			func(raw json.RawMessage) error {
				// Written through as it arrived. The bus is the
				// schema, and re-encoding it here would be a second
				// place for the shape to drift.
				fmt.Println(string(raw))
				return nil
			})
		if err != nil && ctx.Err() == nil {
			cli.Fatalf("%v", err)
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "halite-hub event: unknown subcommand %q\n\n%s", args.Positional[0], eventUsage)
	return 2
}
