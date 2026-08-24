package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// runRunner is `halite-hub runner <module.function> [args]`, the old
// `salt-run`.
//
// A runner runs on the hub, so this command is a request to the hub
// rather than work done here — including when it is typed on the hub
// itself. `run` makes the same argument: an operator authenticates with
// a certificate, and "logged in on the hub" is not a credential.
func runRunner(args *cli.Args) int {
	if len(args.Positional) == 0 {
		fmt.Fprint(os.Stderr, runnerUsage)
		return 2
	}

	switch args.Positional[0] {
	case "list":
		return runnerList(args)
	case "doc":
		return runnerDoc(args)
	}

	fun := args.Positional[0]
	if !strings.Contains(fun, ".") {
		cli.Fatalf("%q is not a runner; a runner is named module.function, as in manage.up", fun)
	}

	kwargs := map[string]any{}
	for _, e := range args.Kwargs.Entries() {
		if k, ok := e.Key.(string); ok {
			kwargs[k] = e.Val
		}
	}

	client := operatorClient(args)
	res, err := client.Runner(context.Background(), transport.RunnerRequest{
		Fun:   fun,
		Arg:   args.Positional[1:],
		Kwarg: kwargs,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}

	if !res.Success {
		fmt.Fprintf(os.Stderr, "%s: %s\n", res.Fun, res.Error)
		return 1
	}

	format, err := cli.ParseFormat(args.Flag("out", "nested"))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	decoded, err := value.DecodeJSON(res.Return)
	if err != nil {
		cli.Fatalf("the hub's answer is not readable: %v", err)
	}
	if err := cli.Write(os.Stdout, decoded, format, 2); err != nil {
		cli.Fatalf("%v", err)
	}
	return 0
}

// runnerList prints the inventory, built and pending alike.
//
// It answers from this binary rather than from the hub. The inventory
// is a property of the build, and an operator asking what runners exist
// is often doing so because they cannot reach the hub.
func runnerList(args *cli.Args) int {
	reg := hub.NewRunners()
	sigs := reg.Signatures()

	format, err := cli.ParseFormat(args.Flag("out", "nested"))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if format == cli.JSON || format == cli.YAML {
		out := value.NewMap(0)
		for _, name := range reg.Names() {
			entry := value.NewMap(2)
			sig, _ := sigs.Lookup(name)
			entry.Set("doc", sig.Doc)
			if when, pending := reg.Pending(name); pending {
				entry.Set("pending", when)
			}
			out.Set(name, entry)
		}
		if err := cli.Write(os.Stdout, out, format, 2); err != nil {
			cli.Fatalf("%v", err)
		}
		return 0
	}

	byModule := map[string][]string{}
	for _, name := range reg.Names() {
		module, _, _ := strings.Cut(name, ".")
		byModule[module] = append(byModule[module], name)
	}
	modules := make([]string, 0, len(byModule))
	for m := range byModule {
		modules = append(modules, m)
	}
	sort.Strings(modules)

	for _, module := range modules {
		fmt.Printf("%s:\n", module)
		for _, name := range byModule[module] {
			sig, _ := sigs.Lookup(name)
			if when, pending := reg.Pending(name); pending {
				fmt.Printf("  %-28s %s [not built: %s]\n", name, sig.Doc, when)
				continue
			}
			fmt.Printf("  %-28s %s\n", name, sig.Doc)
		}
	}
	return 0
}

// runnerDoc prints one runner's signature.
func runnerDoc(args *cli.Args) int {
	if len(args.Positional) < 2 {
		cli.Fatalf("runner doc needs a name: halite-hub runner doc manage.up")
	}
	name := args.Positional[1]
	reg := hub.NewRunners()
	sig, ok := reg.Signatures().Lookup(name)
	if !ok {
		cli.Fatalf("%s is not a runner this build ships; `halite-hub runner list` says what is", name)
	}
	fmt.Print(sig.Describe())
	if when, pending := reg.Pending(name); pending {
		fmt.Printf("\nNot built yet: %s.\n", when)
	}
	return 0
}

var runnerUsage = `halite-hub runner — run a function on the hub itself

Usage:
  halite-hub runner <module.function> [args] [key=value]...
  halite-hub runner list
  halite-hub runner doc <module.function>

Examples:
  halite-hub runner manage.status
  halite-hub runner jobs.lookup_jid 20260823T101500.000000
  halite-hub runner event.send halite/deploy/done data='{"version":"1.2"}'

A runner is authorized by the ` + "`runners:`" + ` list of the caller's role, not by
` + "`functions:`" + `, because permission to ask the hub a question and permission
to run a command on every node are different permissions. SPEC 23.5.
`
