// Command halite-hub is the central service, the file server, the pillar
// compiler, and the full operator command line, in one binary. SPEC
// section 2.2.
//
// This build carries the phase 0 surface: the tools that audit a tree
// before a hub exists. `serve`, `run`, `keys`, and the rest arrive with
// the transport in phase 2 and say so rather than failing obscurely.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/migrate"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

const usage = `halite-hub — the central service and the operator command line

Usage:
  halite-hub migrate <tree>      audit an existing Salt tree and report
  halite-hub lint <path>...      render and parse a file without executing
  halite-hub version             print the build identity

Available with the transport, in phase 2 (SPEC section 32):
  serve, run, runner, orch, keys, files, ssh, event, jobs

migrate flags:
  --pillar-root <dir>  audit a separate pillar tree with pillar rules
  --config <path>      also translate a Salt configuration file, repeatable
  --out <format>       summary (default) or json
  --indent <n>         indent for json output
  --fail-on <level>    exit non-zero at blocking (default), review, or note
  --cmd-default-shell  audit as though the nodes will set cmd_default_shell,
                       which is the transition of SPEC section 15.2
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

	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Println("halite-hub " + version.String())
	case "help", "--help", "-h":
		fmt.Print(usage)
	case "migrate":
		os.Exit(runMigrate(args))
	case "lint":
		os.Exit(runLint(args))
	case "serve", "run", "runner", "orch", "keys", "files", "ssh", "event", "jobs":
		cli.Fatalf("`%s` needs the transport, which arrives in phase 2 (SPEC section 32). "+
			"`migrate` and `lint` work today and are the tools for measuring a tree before then.", os.Args[1])
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

	var configFiles []string
	if v := args.Flag("config", ""); v != "" && v != "true" {
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

	problems := 0
	for _, path := range args.Positional {
		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			problems++
			continue
		}
		res, err := render.Render(src, render.Options{
			File:   path,
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
			fmt.Println(w.String())
			problems++
		}
		if err != nil {
			fmt.Println(err.Error())
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
