package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/extconform"
)

// `extensions verify`, the conformance harness of SPEC 24.
//
// `run` tells an author whether their extension answered. This tells
// them whether it is right — which is a different question, and the one
// somebody who wrote an extension in another language has had no way to
// ask. They found out from whatever the host happened to complain
// about, and a host is written to run extensions rather than to
// diagnose them.

// verifyExtension is `halite-hub extensions verify`.
func verifyExtension(args *cli.Args) int {
	if len(args.Positional) < 2 {
		fmt.Fprint(os.Stderr, extensionsUsage)
		return cli.ExitUsage
	}
	path, err := resolveExtensionPath(args.Positional[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "halite-hub extensions verify: %v\n", err)
		return cli.ExitUsage
	}

	kwargs, err := decodeRunJSON(args.Flag("kwargs", ""))
	if err != nil {
		fmt.Fprintf(os.Stderr, "halite-hub extensions verify: --kwargs: %v\n", err)
		return cli.ExitUsage
	}

	// Not `run`'s default. Every exchange after the handshake is one
	// round trip against a process that has already answered, and there
	// are a dozen of them: at 60s each a run against an extension that
	// hangs takes a quarter of an hour, which is a harness nobody uses.
	//
	// The handshake is the exception, and has its own allowance: its
	// wait includes starting the process, which on a loaded machine is
	// not the extension's doing. See extconform.DefaultStartTimeout.
	timeout, ok := verifyDuration("timeout", args.Flag("timeout", ""), extconform.DefaultTimeout)
	if !ok {
		return cli.ExitUsage
	}
	startTimeout, ok := verifyDuration("start-timeout", args.Flag("start-timeout", ""),
		extconform.DefaultStartTimeout)
	if !ok {
		return cli.ExitUsage
	}
	opts := extconform.Options{
		Path:         path,
		Kind:         args.Flag("kind", ""),
		Function:     args.Flag("function", ""),
		Kwargs:       kwargs,
		Timeout:      timeout,
		StartTimeout: startTimeout,
	}
	// The whole run is several sessions. A context shorter than the
	// harness's own worst case kills the extension mid-rule, and that
	// rule then reports an exit the extension never made -- so it is
	// sized from the same arithmetic rather than a second guess at it.
	ctx, cancel := context.WithTimeout(context.Background(), opts.Budget())
	defer cancel()

	results, err := extconform.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "halite-hub extensions verify: %v\n", err)
		return 1
	}

	if args.Bool("json", false) {
		encoded, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(encoded))
	} else {
		printConformance(args.Positional[1], results)
	}
	if extconform.Failed(results) {
		return 1
	}
	return 0
}

// verifyDuration reads one positive duration flag, reporting a bad one.
//
// It takes the value rather than the flag's name so that each
// args.Flag call stays spelt out at its call site, which is where
// TestEveryFlagIsDocumentedAndParsed looks for it.
func verifyDuration(name, raw string, fallback time.Duration) (time.Duration, bool) {
	if raw == "" {
		return fallback, true
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		fmt.Fprintf(os.Stderr, "halite-hub extensions verify: --%s %q is not a duration\n", name, raw)
		return 0, false
	}
	return parsed, true
}

// printConformance reports a run the way `doctor` reports one.
func printConformance(name string, results []extconform.Result) {
	fmt.Printf("halite-hub extensions verify — %s\n\n", name)
	for _, r := range results {
		fmt.Printf("  %-4s  %-46s %s\n", r.Status, r.Rule, r.Title)
		if r.Detail != "" {
			fmt.Printf("        %s\n", wrapConformance(r.Detail))
		}
		// The reason only on a failure. On a pass it is noise, and a
		// report that is mostly noise is one nobody reads to the end.
		if r.Status == extconform.Fail && r.Why != "" {
			fmt.Printf("        %s\n", wrapConformance(r.Why))
		}
	}

	pass, fail, skip := extconform.Counts(results)
	fmt.Printf("\n  %d pass, %d fail, %d skip\n", pass, fail, skip)
	if skip > 0 {
		// Said out loud. A skip is not a pass, and a report where the
		// two look alike is one that overstates what it established.
		fmt.Println("\n  A skip is not a pass: each says what could not be established.")
	}
	if fail == 0 && skip == 0 {
		fmt.Println("\n  Nothing here checks what the extension does, only that it speaks the")
		fmt.Println("  protocol. A conforming extension can still be wrong about its own job.")
	}
}

// wrapConformance folds a long line to fit a terminal, indented under
// its rule.
func wrapConformance(text string) string {
	const width = 68
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	var out strings.Builder
	line := 0
	for i, word := range words {
		if line > 0 && line+1+len(word) > width {
			out.WriteString("\n        ")
			line = 0
		} else if i > 0 {
			out.WriteString(" ")
			line++
		}
		out.WriteString(word)
		line += len(word)
	}
	return out.String()
}
