// Command halite-api is the HTTP API service. SPEC section 2.2.
//
// It is a separate binary running under a separate account, and it is a
// client of the hub rather than a component of it. In Salt the API process
// loads the central service's own configuration and calls into its
// internals, so an API flaw is a flaw in the central service. Here the API
// is a client with a scoped identity, and its worst case is bounded by its
// RBAC policy. SPEC section 5.2.
//
// This build serves most of SPEC section 22: login, logout, token
// introspection, the module schema, health, the execution endpoints, the
// event stream as SSE and as a WebSocket, signed webhook ingress, and
// the Prometheus exposition. OIDC, LDAP, returners, and the bridge
// protocol are the rest of phase 4.
package main

import (
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/version"
)

var usage = `halite-api — the HTTP API service

Usage:
  halite-api serve              run the HTTP API
  halite-api token <subcommand> issued tokens: list, show, revoke, prune
  halite-api account <sub>      local accounts: hash, list
  halite-api version            print the build identity

Common flags:
  --help               describe the program without running a command
  --config <path>      configuration file, default <root>/api.yaml
  --root <dir>         configuration root, default ` + config.DefaultRoot + `
  --out <format>       nested (default), json, yaml, or txt

serve flags:
  --listen <addr>      listen address, default :4511
  --tls-cert <path>    the certificate this service presents
  --tls-key <path>     its key
  --hub <address>      the hub this service is a client of
  --pki-dir <dir>      key material, default ` + config.DefaultPKIDir + `
  --as <name>          which operator certificate to present to the hub
  --cert <path>        an operator certificate, instead of --as
  --key <path>         its key
  --server-name <name> the name to verify in the hub's certificate
  --accounts <path>    the local account file
  --policy <path>      the RBAC policy file
  --log-level <level>  error, warn, info (default), debug, or trace
  --log-fmt <format>   json (default) or console

token flags:
  --limit <n>          how many tokens to list, default 20

account flags:
  --iterations <n>     PBKDF2 cost for a new hash
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args, err := cli.Parse(os.Args[2:])
	if err != nil {
		cli.Fatalf("%v", err)
	}
	// `--help` after any subcommand describes the program rather than
	// running it. A command that opens a listener needs a way to be
	// asked what it is without doing it.
	if args.Bool("help", false) {
		fmt.Print(usage)
		os.Exit(0)
	}

	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Println(version.Full("halite-api"))
	case "help", "--help", "-h":
		fmt.Print(usage)
	case "serve":
		os.Exit(runServe(args))
	case "token":
		os.Exit(runToken(args))
	case "account":
		os.Exit(runAccount(args))
	case "policy":
		cli.Fatalf("the policy is the hub's; `halite-hub policy show` and " +
			"`halite-hub policy test` read the same file this service does.")
	default:
		fmt.Fprintf(os.Stderr, "halite-api: unknown subcommand %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
