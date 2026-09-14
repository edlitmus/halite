package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/fileserver"
)

const extensionsUsage = `halite-hub extensions — the signed extensions this hub runs

Usage:
  halite-hub extensions list                 what is installed, and what it provides
  halite-hub extensions sync                 fetch _ext/ from the tree into the cache
  halite-hub extensions run <path> [<fn>]    run one straight from a path, for writing it

Synchronization fetches and does not load. What this hub runs does not
change until it is restarted, so publishing an extension into the tree
cannot change a running hub behaviour (SPEC section 24.5).

run starts the file where it lies: no bundle, no signature, no cache,
no restart. It is the loop for writing an extension, and it is not how
one reaches a node -- it verifies nothing about where the file came
from. docs/extensions.md walks through the packaging that does.

Flags for sync and list:
  --env <name>       the environment to read _ext/ from, default base

Flags for run:
  --kwargs <json>    keyword arguments for the call
  --args <json>      positional arguments
  --kind <kind>      the kind the host asks for; empty accepts any
  --node-id <id>     what the call context says, default dev
  --env <name>       what the call context says, default base
  --test             a run that must change nothing
  --timeout <dur>    how long one call may take, default 60s
  --sandbox          apply the confinement a node would
  --declare <list>   what --sandbox grants: network, root
  --user <name>      the account --sandbox drops to
  --group <name>     its group
  --json             print only the result
`

// runExtensions is the hub's side of SPEC 24.5.
func runExtensions(args *cli.Args) int {
	if len(args.Positional) == 0 {
		fmt.Fprint(os.Stderr, extensionsUsage)
		return 2
	}
	sub := args.Positional[0]
	if sub == "help" || sub == "--help" || sub == "-h" {
		fmt.Print(extensionsUsage)
		return 0
	}
	// run reads no configuration: it starts a file and speaks to it,
	// which is the whole point of having it. Dispatched before the
	// configuration is loaded so that an author with no hub.yaml -- who
	// is most authors -- can use it.
	if sub == "run" {
		return runExtensionDirectly(args)
	}

	// Not openHub: that resolves the enrollment CA, and these commands
	// have nothing to do with one. A hub whose CA has not been created
	// yet -- which is every hub before its first `serve` -- could
	// otherwise not fetch the extensions it is about to need.
	h := openHubForConfig(args)

	switch sub {
	case "list":
		return extensionsList(h)
	case "sync":
		return extensionsSync(h, args.Flag("env", "base"))
	default:
		fmt.Fprintf(os.Stderr, "halite-hub extensions: unknown subcommand %q\n\n%s", sub, extensionsUsage)
		return 2
	}
}

// extensionsList reports what the cache holds.
//
// Read from the cache rather than from the tree, and verified on the
// way, so this answers "what would this hub run" rather than "what has
// somebody published".
func extensionsList(h *hubContext) int {
	runtime := h.openExtensions()
	defer runtime.Close()

	names := runtime.Names()
	if len(names) == 0 {
		fmt.Printf("No extensions are installed in %s.\n", h.extensionDir())
		return 0
	}
	sort.Strings(names)
	for _, name := range names {
		loaded, ok := runtime.Get(name)
		if !ok {
			continue
		}
		m := loaded.Bundle.Manifest
		fmt.Printf("%s %s (%s)\n", m.Name, m.Version, m.Kind)
		if len(m.Declares) > 0 {
			fmt.Printf("  declares: %v\n", m.Declares)
		}
		// The handshake is what says which functions it really has; the
		// manifest says what it is allowed to need. Starting one here
		// costs a process and answers the question the operator asked.
		if err := h.warmExtension(loaded); err != nil {
			fmt.Printf("  does not start: %v\n", err)
			continue
		}
		for _, sig := range loaded.Functions {
			fmt.Printf("  %s()\n", sig.Function)
		}
	}
	return 0
}

// extensionsSync fetches bundles out of the tree.
func extensionsSync(h *hubContext, env string) int {
	roots := h.cfg.Roots("file_roots")
	if len(roots) == 0 {
		fmt.Fprintln(os.Stderr,
			"halite-hub extensions sync: no file_roots, so there is no tree to read `_ext/` from")
		return 1
	}
	report, err := h.syncExtensions(fileserver.NewRoots(roots), env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "halite-hub extensions sync: %v\n", err)
		return 1
	}
	for _, e := range report.Errors {
		fmt.Fprintf(os.Stderr, "  %s\n", e)
	}
	if len(report.Changes) == 0 {
		fmt.Printf("Nothing under _ext/ in %s.\n", env)
		return 0
	}
	for _, c := range report.Changes {
		switch c.Status {
		case "refused":
			fmt.Printf("  refused   %s %s: %s\n", c.Name, c.Version, c.Reason)
		case "fetched":
			// The Merkle root is printed because it is what an operator
			// copies into `extension_pins`, and having to go and find
			// it is why pins get left unset.
			fmt.Printf("  fetched   %s %s\n            root: %s\n", c.Name, c.Version, c.Root)
		default:
			fmt.Printf("  unchanged %s %s\n", c.Name, c.Version)
		}
	}
	if report.Changed() {
		fmt.Println("\nFetched, not loaded. Restart the hub to run what was fetched.")
	}
	if len(report.Errors) > 0 {
		return 1
	}
	return 0
}
