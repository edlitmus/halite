package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/cli"
)

// runMetrics is `halite-hub metrics`, the operator's scrape.
//
// The hub speaks its own ALPN protocol and requires a client
// certificate, so a scraper cannot reach `/v1/metrics` with an ordinary
// HTTP client. That is deliberate — an unauthenticated scrape endpoint
// on a control plane tells anyone who asks how many nodes it has and
// when a deployment went out — and it means the operator needs a way in,
// which is this.
//
// An estate that wants a Prometheus scrape points it at `halite-api`,
// whose `/v1/metrics` takes a bearer token and merges the hub's
// exposition with its own.
func runMetrics(args *cli.Args) int {
	client := operatorClient(args)
	out, err := client.Metrics(context.Background())
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if filter := args.Flag("filter", ""); filter != "" && filter != "true" {
		out = filterExposition(out, filter)
	}
	fmt.Print(out)
	return 0
}

// filterExposition keeps the families whose name contains the substring,
// with the `# HELP` and `# TYPE` lines that belong to them.
//
// A hub exposes a few hundred lines and the question is usually about
// one subsystem. Filtering here rather than through grep keeps the
// declaration lines, which are what say whether a counter exists at all.
func filterExposition(out, want string) string {
	var kept []string
	keeping := false
	for _, line := range strings.Split(out, "\n") {
		if name, ok := declarationName(line); ok {
			keeping = strings.Contains(name, want)
		}
		if keeping && line != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		fmt.Fprintf(os.Stderr, "no metric family matches %q\n", want)
		return ""
	}
	return strings.Join(kept, "\n") + "\n"
}

// declarationName reads the family name off a `# HELP` or `# TYPE` line.
func declarationName(line string) (string, bool) {
	for _, prefix := range []string{"# HELP ", "# TYPE "} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			name, _, _ := strings.Cut(rest, " ")
			return name, true
		}
	}
	return "", false
}
