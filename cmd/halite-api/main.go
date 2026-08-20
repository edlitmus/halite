// Command halite-api is the HTTP API service. SPEC section 2.2.
//
// It is a separate binary running under a separate account, and it is a
// client of the hub rather than a component of it. In Salt the API process
// loads the central service's own configuration and calls into its
// internals, so an API flaw is a flaw in the central service. Here the API
// is a client with a scoped identity, and its worst case is bounded by its
// RBAC policy. SPEC section 5.2.
//
// The service itself arrives in phase 4 (SPEC section 32), together with
// the transport it is a client of.
package main

import (
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/version"
)

const usage = `halite-api — the HTTP API service

Usage:
  halite-api version    print the build identity

Available in phase 4 (SPEC section 32):
  serve, token, policy
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Println("halite-api " + version.String())
	case "help", "--help", "-h":
		fmt.Print(usage)
	case "serve", "token", "policy":
		cli.Fatalf("`%s` arrives in phase 4, with the transport it is a client of (SPEC section 32).", os.Args[1])
	default:
		fmt.Fprintf(os.Stderr, "halite-api: unknown subcommand %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
