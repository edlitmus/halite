package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/transport"
)

// runEvent is `halite-node event send <tag> [json]`, the old
// `salt-call event.send`.
//
// The hub namespaces the tag under this node, so what arrives on the
// bus is `halite/node/<this node>/<tag>` whatever is typed here. A node
// that could write any tag could fire the event a reactor watches for,
// and Salt's reactor runs with full master privilege. // lexicon:allow
func runEvent(args *cli.Args) int {
	if len(args.Positional) == 0 {
		cli.Fatalf("event needs a subcommand; try `event send <tag> '{\"key\":\"value\"}'`")
	}
	if args.Positional[0] != "send" {
		cli.Fatalf("event has no subcommand %q; there is `send`", args.Positional[0])
	}
	if len(args.Positional) < 2 {
		cli.Fatalf("event send needs a tag")
	}
	tag := args.Positional[1]

	var data json.RawMessage
	if len(args.Positional) > 2 {
		body := strings.Join(args.Positional[2:], " ")
		if !json.Valid([]byte(body)) {
			cli.Fatalf("the event data is not JSON: %s", body)
		}
		data = json.RawMessage(body)
	} else if args.Kwargs.Len() > 0 {
		// `event send deploy/done version=1.2` is the shape muscle
		// memory reaches for, so the keyword arguments become the
		// payload when no JSON is given.
		encoded, err := json.Marshal(kwargsToMap(args))
		if err != nil {
			cli.Fatalf("%v", err)
		}
		data = encoded
	}

	n := setup(args)
	client, _ := n.hubClient(args)
	if client.Cert == nil {
		cli.Fatalf("this node is not enrolled; `halite-node enroll` is the first step")
	}
	res, err := client.SendEvent(context.Background(), transport.EventRequest{
		Tag:         tag,
		Data:        data,
		Correlation: args.Flag("correlation", ""),
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	fmt.Fprintf(os.Stdout, "%s\n", res.Tag)
	return 0
}

// kwargsToMap turns `key=value` arguments into a payload.
func kwargsToMap(args *cli.Args) map[string]any {
	out := map[string]any{}
	for _, e := range args.Kwargs.Entries() {
		if k, ok := e.Key.(string); ok {
			out[k] = e.Val
		}
	}
	return out
}
