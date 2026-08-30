// Command halite-hub is the central service, the file server, the pillar
// compiler, and the full operator command line, in one binary. SPEC
// section 2.2.
//
// This build carries the control plane of SPEC section 6, the fleet
// commands of section 9, the runners of section 19.2, orchestration,
// reactors, the mine, metrics, and the tools that audit a tree before a
// hub exists. `files` and `ssh` are not built and say so rather than
// failing obscurely.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/migrate"
	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

var usage = `halite-hub — the central service and the operator command line

Usage:
  halite-hub serve               run the control plane
  halite-hub keys <subcommand>   enrollment and the key lifecycle
  halite-hub run <target> <fun>  run a function across the fleet
  halite-hub runner <fun>        run a function on the hub itself
  halite-hub orch <subcommand>   orchestration, which runs on the hub
  halite-hub jobs <subcommand>   the job cache
  halite-hub policy <show|test>  the RBAC policy, and what it decides
  halite-hub event <listen|tags> the event bus
  halite-hub metrics             the Prometheus exposition
  halite-hub ssh <target> <fun>  run on a machine with no agent
  halite-hub migrate <tree>      audit an existing Salt tree and report
  halite-hub lint <path>...      render and parse a file without executing
  halite-hub version             print the build identity

Still to come (SPEC section 32):
  files, the push in the other direction from salt-cp

Common flags:
  --help               describe the program without running a command
  --config <path>      configuration file, default <root>/hub.yaml
  --root <dir>         configuration root, default ` + config.DefaultRoot + `
  --out <format>       summary (default) or json

serve flags:
  --listen <addr>      listen address, default :4510
  --relay              serve nodes and proxy them to an upstream hub (SPEC 5.3)
  --upstream <address> the hub this relay presents itself to
  --upstream-pki-dir <dir>  the key material this relay enrolled upstream with
  --upstream-cert <path>    its certificate, instead of --upstream-pki-dir
  --upstream-key <path>     its key
  --upstream-server-name <name>  the name to verify in the upstream's certificate
  --pki-dir <dir>      key material, default ` + config.DefaultPKIDir + `
  --names <a,b>        names to issue the hub's certificate for
  --log-level <level>  error, warn, info (default), debug, or trace
  --log-fmt <format>   json (default) or console

run flags:
  --batch <n|n%>       run against this many nodes at a time; the hub owns
                       the batch, so closing the terminal does not stop it
  --batch-wait <dur>   settle time between batches
  --batch-safe-limit <n>  stop the run once this many nodes have failed
  --batch-timeout <dur>   how long one batch waits for its returns
  --subset <n>         run against a random n of the matched set
  --progress           report dispatched, returned, and outstanding as it goes
  --async              print the jid and return at once
  --timeout <dur>      how long to gather returns, default 5m
  --test               run every state in test mode, changing nothing
  --env <name>         the environment the job names
  --ttl <dur>          how long the job may be run, default 15m
  --offline <policy>   skip (default), queue, or require
  --as <name>          which operator certificate to present
  --cert <path>        an operator certificate, instead of --as
  --key <path>         its key
  --hub <address>      the hub to reach, default localhost
  --server-name <name> the name to verify in the hub's certificate
  -L -E -G -P -I -J -S -N -C   the target kinds of SPEC section 8

runner flags:
  --as <name>          which operator certificate to present
  --hub <address>      the hub to reach, default localhost

orch flags:
  --env <name>         the environment to compile the orchestration from
  --pillar <json>      pillar overrides the run compiles with
  --from <step>        which step orch resume picks up at
  --limit <n>          how many runs orch list shows
  --test               report what each step would do without dispatching

jobs flags:
  --limit <n>          how many jobs to list, default 20
  --as <name>          which operator certificate to present, for resume

policy flags:
  --runner             evaluate the function as a runner rather than a job
  --kwarg <k=v>        an argument to include, repeatable as a comma list

ssh flags:
  --roster <backend>   flat (default), sshconfig, cache, or ansible
  --roster-file <path> the roster file
  --thin <path>        the halite-node binary to push
  --clean              remove the cached binary before and after
  --ssh-concurrency <n>  how many targets at once, default 8

metrics flags:
  --filter <substring> only families whose name contains this
  --as <name>          which operator certificate to present
  --hub <address>      the hub to reach, default localhost

event flags:
  --tag <glob>         a tag to follow, repeatable as a comma list
  --from <position>    latest (default), earliest, or an offset
  --once               read what is there and stop, rather than following

migrate flags:
  --pillar-root <dir>  audit a separate pillar tree with pillar rules
  --salt-config <path> also translate a Salt configuration file, repeatable
  --indent <n>         indent for json output
  --fail-on <level>    exit non-zero at blocking (default), review, or note
  --cmd-default-shell  audit as though the nodes will set cmd_default_shell,
                       which is the transition of SPEC section 15.2
  --bridge-skeleton <dir>  write a Go bridge skeleton for each custom Python
                       module found, one command per module (SPEC 24.6)
`

// subUsage names the text that documents each subcommand's own flags,
// so an unknown flag is judged against the command actually asked for
// rather than against everything the program can do.
var subUsage = map[string][]string{
	"keys":   {keysUsage},
	"jobs":   {jobsUsage},
	"policy": {policyUsage},
	"event":  {eventUsage},
	"runner": {runnerUsage},
	"orch":   {orchUsage},
	"ssh":    {sshUsage},
}

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
	// running it. A command that starts a listener or reaches for key
	// material needs a way to be asked what it is without doing it.
	if args.Bool("help", false) {
		fmt.Print(usage)
		os.Exit(0)
	}
	cli.RejectUnknownFlags(args, "halite-hub "+os.Args[1],
		append([]string{usage}, subUsage[os.Args[1]]...)...)

	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Println(version.Full("halite-hub"))
	case "help", "--help", "-h":
		fmt.Print(usage)
	case "migrate":
		os.Exit(runMigrate(args))
	case "lint":
		os.Exit(runLint(args))
	case "serve":
		os.Exit(runServe(args))
	case "keys":
		os.Exit(runKeys(args))
	case "run":
		os.Exit(runRun(args))
	case "jobs":
		os.Exit(runJobs(args))
	case "policy":
		os.Exit(runPolicy(args))
	case "event":
		os.Exit(runEvent(args))
	case "runner":
		os.Exit(runRunner(args))
	case "metrics":
		os.Exit(runMetrics(args))
	case "orch":
		os.Exit(runOrch(args))
	case "files":
		cli.Fatalf("`files` is the hub-side file push, which is not built (SPEC section 32). " +
			"A node fetches from `salt://` today; this is the push in the other direction.")
	case "ssh":
		os.Exit(runSSH(args))
	default:
		fmt.Fprintf(os.Stderr, "halite-hub: unknown subcommand %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

// runMigrate is the report of SPEC section 28.5. It produces a report, not
// a rewritten tree, because a tool that silently rewrites a production
// state tree is not trustworthy.
func runMigrate(args *cli.Args) int {
	if len(args.Positional) == 0 {
		cli.Fatalf("migrate needs a tree to audit")
	}

	// `--config` names this program's own configuration everywhere else,
	// including `halite-hub lint` two functions down. Using it here for
	// "a Salt file to translate" meant one flag with two meanings inside
	// one program, and the wrong one silently: pointing `--config` at
	// hub.yaml asked the audit to translate it as Salt.
	var configFiles []string
	if v := args.Flag("salt-config", ""); v != "" && v != "true" {
		configFiles = strings.Split(v, ",")
	}

	registries := builtin.New()
	rep, err := migrate.Run(migrate.Options{
		Root:        args.Positional[0],
		PillarRoot:  args.Flag("pillar-root", ""),
		ConfigFiles: configFiles,
		// The registries this build ships. The audit used to withhold
		// them, on the reasoning that a later build will have more
		// modules and judging a tree against an incomplete set reports
		// work that does not exist. The cost of that was worse: a real
		// tree with twenty-seven compilation errors was reported clean,
		// because the audit was not looking at the states at all. The
		// question an operator asks is "will this run", and the only
		// build that can answer it is this one.
		Registry:      registries.Exec.Signatures(),
		StateRegistry: registries.States.Signatures(),
		DefaultShell:  args.Bool("cmd-default-shell", false),
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}

	format, err := cli.ParseFormat(args.Flag("out", "summary"))
	if args.Flag("out", "summary") == "summary" {
		fmt.Print(rep.Summary())
	} else {
		if err != nil {
			cli.Fatalf("%v", err)
		}
		indent := 0
		fmt.Sscanf(args.Flag("indent", "0"), "%d", &indent)
		if err := cli.Write(os.Stdout, rep.JSON(), format, indent); err != nil {
			cli.Fatalf("%v", err)
		}
	}

	// SPEC 24.6: the report detects a formula's `_modules/` and friends,
	// and this writes the bridge skeletons. Separate from the report on
	// purpose — `migrate` produces a report and does not rewrite a
	// tree, and writing Go files somewhere the operator did not ask for
	// would be the same kind of surprise.
	if dir := args.Flag("bridge-skeleton", ""); dir != "" && dir != "true" {
		if err := writeSkeletons(rep, dir); err != nil {
			cli.Fatalf("%v", err)
		}
	}

	counts := rep.Count()
	// An unrecognised level used to fall through to blocking, so
	// `--fail-on reveiw` audited less than it was asked to and said
	// nothing about it. A gate that quietly loosens is worse than no
	// gate.
	switch level := args.Flag("fail-on", "blocking"); level {
	case "note":
		if counts.Total > 0 {
			return 1
		}
	case "review":
		if counts.Blocking+counts.BySeverity[migrate.Review] > 0 {
			return 1
		}
	case "blocking":
		if counts.Blocking > 0 {
			return 1
		}
	default:
		cli.Fatalf("--fail-on %q is not a level; try blocking, review, or note", level)
	}
	return 0
}

// runLint renders and parses without executing.
func runLint(args *cli.Args) int {
	if len(args.Positional) == 0 {
		cli.Fatalf("lint needs a path")
	}
	cfg, err := config.Load(config.Hub, config.LoadOptions{
		Path:         args.Flag("config", ""),
		Root:         args.Flag("root", config.DefaultRoot),
		AllowMissing: true,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}

	// `lint` renders, and rendering a `#!yaml|gpg` file decrypts, so
	// this program can hold a secret even though it never applies a
	// state. SPEC 26.1 puts the redactor at the sink; here the sinks are
	// this loop's output and Fatalf.
	secrets := redact.New()
	cli.Redact = secrets.Scrub

	problems := 0
	for _, path := range args.Positional {
		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			problems++
			continue
		}
		res, err := render.Render(src, render.Options{
			File:     path,
			OnSecret: secrets.Add,
			// The gpg settings are read by both programs, and a lint
			// that could not find the keyring would report a file as
			// unrenderable when the node renders it perfectly well.
			GPG: render.GPGOptions{
				Binary:  cfg.String("gpg_binary", ""),
				Home:    cfg.String("gpg_home", ""),
				Timeout: gpgTimeout(cfg),
			},
			SLS:    strings.TrimSuffix(path, ".sls"),
			Env:    cfg.String("env", "base"),
			Grains: value.NewMap(0),
			Pillar: value.NewMap(0),
			Config: cfg.Redacted(),
			// Lint runs permissive on purpose: a tree is being checked
			// for structure, and this run has neither the node's grains
			// nor its pillar, so a strict undefined here would report a
			// problem that does not exist on the node.
			Undefined: 1,
		})
		for _, w := range res.Warnings {
			fmt.Println(secrets.Scrub(w.String()))
			problems++
		}
		if err != nil {
			fmt.Println(secrets.Scrub(err.Error()))
			problems++
			continue
		}
		fmt.Printf("%s: renders and parses; pipeline %s\n", path, strings.Join(res.Pipeline, "|"))
	}
	if problems > 0 {
		return 1
	}
	return 0
}

// gpgTimeout reads the per-decryption bound of SPEC section 12.6.
func gpgTimeout(cfg *config.Config) time.Duration {
	d, err := time.ParseDuration(cfg.String("gpg_timeout", "30s"))
	if err != nil {
		return 30 * time.Second
	}
	return d
}

// writeSkeletons generates a bridge for every custom Python module the
// audit found.
//
// One command per module, each a directory with a main.go, because that
// is what `go build` takes and what an operator will put in a bundle. A
// file that already exists is not overwritten: the second run of a
// migration should not discard the work done after the first.
func writeSkeletons(rep *migrate.Report, dir string) error {
	skeletons := migrate.Skeletons(rep.PyModules, rep.PyKinds)
	if len(skeletons) == 0 {
		fmt.Printf("no custom Python modules to generate a bridge for\n")
		return nil
	}
	written, kept := 0, 0
	for _, skeleton := range skeletons {
		path := filepath.Join(dir, filepath.FromSlash(skeleton.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("%s exists; leaving it alone\n", path)
			kept++
			continue
		}
		if err := os.WriteFile(path, []byte(skeleton.Source), 0o644); err != nil {
			return err
		}
		fmt.Printf("%s (%s, %d function(s) from %s)\n",
			path, skeleton.Kind, len(skeleton.Module.Functions), skeleton.Module.File)
		written++
	}
	fmt.Printf("\n%d skeleton(s) written, %d left alone. Each is a starting point: every\n"+
		"function returns an error until it is written, so a bridge that was\n"+
		"generated and forgotten fails loudly rather than answering nothing.\n", written, kept)
	return nil
}
