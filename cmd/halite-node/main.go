// Command halite-node is the endpoint agent and the local executor. It
// replaces three of Salt's entry points in one binary: the agent, the
// local caller, and the proxy. SPEC section 2.2.
//
// This build carries the phase 1 surface: everything a node can do about
// its own tree without a hub. `serve` and `event` arrive with the
// transport in phase 2 and say so rather than failing obscurely.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

var usage = `halite-node — the managed endpoint agent and local executor

Usage:
  halite-node call <module.function> [args...]   run one module function
  halite-node state <subcommand> [args...]       compile or apply state
  halite-node grains <items|item|get|ls> [key]   read this node's grains
  halite-node pillar <items|item|get> [key]      read this node's pillar
  halite-node lint <path>                        render and parse without executing
  halite-node version                            print the build identity
  halite-node serve                              connect to a hub (phase 2)

Common flags:
  --local              work from local roots rather than through a hub
  --config <path>      configuration file, default <root>/node.yaml
  --root <dir>         configuration root, default ` + config.DefaultRoot + `
  --file-root <dir>    a state root, repeatable; implies --local
  --pillar-root <dir>  a pillar root, repeatable; implies --local
  --env <name>         environment, default base
  --id <node-id>       override the node identity
  --out <format>       nested (default), json, yaml, txt, quiet
  --indent <n>         indent for json output
  --test               run every state in test mode, changing nothing
  --permissive         allow undefined template names, as Salt does

State subcommands:
  apply [sls...]       apply the highstate, or the named SLS files
  highstate            apply the highstate
  sls <name>...        apply the named SLS files
  show_top             print the top file matches for this node
  show_highstate       print the assembled high state
  show_lowstate        print the ordered low state
  show_sls <name>...   print the high state of the named SLS files
  show_states          print the state IDs that would run
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	sub := os.Args[1]
	args, err := cli.Parse(os.Args[2:])
	if err != nil {
		cli.Fatalf("%v", err)
	}

	switch sub {
	case "version", "--version", "-v":
		fmt.Println("halite-node " + version.String())
	case "help", "--help", "-h":
		fmt.Print(usage)
	case "call":
		os.Exit(runCall(args))
	case "state":
		os.Exit(runState(args))
	case "grains":
		os.Exit(runGrains(args))
	case "pillar":
		os.Exit(runPillar(args))
	case "lint":
		os.Exit(runLint(args))
	case "serve", "event":
		cli.Fatalf("`%s` needs the transport, which arrives in phase 2 (SPEC section 32). "+
			"Everything a node can do about its own tree works today with --local.", sub)
	default:
		fmt.Fprintf(os.Stderr, "halite-node: unknown subcommand %q\n\n%s", sub, usage)
		os.Exit(2)
	}
}

// node holds everything one local invocation needs.
type node struct {
	cfg      *config.Config
	registry *builtin.Registries
	grains   *value.Map
	nodeID   string
	env      string
	test     bool
	format   cli.Format
	indent   int
	files    *fileserver.Roots
	pillars  *fileserver.Roots
	undef    template.UndefinedMode
}

// setup loads configuration, resolves the identity, and collects grains.
func setup(args *cli.Args) *node {
	root := args.Flag("root", config.DefaultRoot)
	cfg, err := config.Load(config.Node, config.LoadOptions{
		Path:         args.Flag("config", ""),
		Root:         root,
		AllowMissing: true,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintln(os.Stderr, "halite-node: "+w)
	}

	format, err := cli.ParseFormat(args.Flag("out", cfg.String("output", "nested")))
	if err != nil {
		cli.Fatalf("%v", err)
	}

	n := &node{
		cfg:      cfg,
		registry: builtin.New(),
		env:      args.Flag("env", cfg.String("env", "base")),
		test:     args.Bool("test", cfg.Bool("test", false)),
		format:   format,
		indent:   int(parseInt(args.Flag("indent", "0"))),
		undef:    template.Strict,
	}
	if args.Bool("permissive", false) || cfg.String("undefined", "strict") == "permissive" {
		n.undef = template.Permissive
	}

	n.nodeID = resolveNodeID(args, cfg)

	g, warnings := grains.Collect(grains.Options{
		NodeID:     n.nodeID,
		StaticFile: root + "/grains",
		GrainsDir:  root + "/grains.d",
		Extra:      cfg.Map("grains"),
		Cloud:      cfg.Bool("cloud_grains", false),
	})
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "halite-node: grains: "+w.String())
	}
	n.grains = g

	// The configuration root is probed first, so that a node whose tree
	// lives beside its configuration — or is symlinked there, which is
	// what an administrator reaches for — needs no file_roots at all.
	// SPEC 27.3's /srv paths follow, with Salt's own so that an existing
	// tree needs no move.
	n.files = fileserver.NewRoots(rootsFrom(args, cfg, "file-root", "file_roots",
		[]string{filepath.Join(root, "state"), "/srv/halite/states", "/srv/salt"}, n.env))
	n.pillars = fileserver.NewRoots(rootsFrom(args, cfg, "pillar-root", "pillar_roots",
		[]string{filepath.Join(root, "pillar"), "/srv/halite/pillar", "/srv/pillar"}, n.env))
	return n
}

// resolveNodeID applies the order of SPEC section 7.2, as far as a local
// run needs it.
func resolveNodeID(args *cli.Args, cfg *config.Config) string {
	if id := args.Flag("id", ""); id != "" {
		return id
	}
	if id := cfg.String("node_id", ""); id != "" {
		return id
	}
	if id := os.Getenv("HALITE_NODE_ID"); id != "" {
		return id
	}
	if b, err := os.ReadFile(filepath.Join(config.DefaultRoot, "node_id")); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	host, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return host
}

// rootsFrom builds the environment-to-directories map from the flags, the
// configuration, and the packaged defaults, in that order of preference.
func rootsFrom(args *cli.Args, cfg *config.Config, flag, key string, defaults []string, env string) map[string][]string {
	if dirs := repeatedFlag(args, flag); len(dirs) > 0 {
		return map[string][]string{env: dirs}
	}
	if configured := cfg.Roots(key); len(configured) > 0 {
		return configured
	}
	// The packaged defaults include Salt's own paths, so an existing tree
	// needs no move. SPEC section 27.3.
	var present []string
	for _, d := range defaults {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			present = append(present, d)
		}
	}
	if len(present) == 0 {
		present = defaults[:1]
	}
	return map[string][]string{env: present}
}

// repeatedFlag reads a flag that may be given more than once. The parser
// keeps only the last, so a comma-separated list is also accepted.
func repeatedFlag(args *cli.Args, name string) []string {
	v := args.Flag(name, "")
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseInt(s string) int64 {
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}

// gpgOptions reads the gpg renderer's settings of SPEC section 12.6.
func (n *node) gpgOptions() render.GPGOptions {
	timeout, err := time.ParseDuration(n.cfg.String("gpg_timeout", "30s"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "halite-node: gpg_timeout: %v; using 30s\n", err)
		timeout = 30 * time.Second
	}
	return render.GPGOptions{
		Binary:  n.cfg.String("gpg_binary", ""),
		Home:    n.cfg.String("gpg_home", ""),
		Timeout: timeout,
	}
}

// compilePillar assembles this node's pillar from the local roots.
func (n *node) compilePillar() *value.Map {
	strategy, _ := value.ParseStrategy(n.cfg.String("pillar_source_merging_strategy", "smart"))
	c := &pillar.Compiler{
		Loader: n.pillars,
		Config: pillar.Config{
			// `salt['pillar.get']` inside a pillar file sees the pillar
			// built so far, so the dispatcher is built per render.
			NewSalt: func(partial *value.Map) template.Dispatcher {
				return exec.TemplateDispatcher{Registry: n.registry.Exec, Context: n.context(partial)}
			},
			Env:           n.env,
			NodeID:        n.nodeID,
			Grains:        n.grains,
			ConfigValues:  n.cfg.Redacted(),
			TrustedGrains: n.cfg.StringSlice("pillar_trusted_grains"),
			Strategy:      strategy,
			MergeLists:    n.cfg.Bool("pillar_merge_lists", false),
			Undefined:     n.undef,
			GPG:           n.gpgOptions(),
			Local:         true,
		},
	}
	out := c.Compile()
	for _, w := range out.Warnings {
		fmt.Fprintln(os.Stderr, "halite-node: pillar: "+w.String())
	}
	if err := out.Err(); err != nil {
		cli.Fatalf("%v", err)
	}
	return out.Pillar
}

// context builds the module execution context.
func (n *node) context(p *value.Map) *exec.Context {
	return &exec.Context{
		Ctx:      context.Background(),
		Grains:   n.grains,
		Pillar:   p,
		Config:   n.cfg.Redacted(),
		NodeID:   n.nodeID,
		Env:      n.env,
		JobID:    newJobID(),
		Test:     n.test,
		Files:    fileserver.NewFetcher(n.files),
		Dispatch: dispatcher{n.registry.Exec},
		Runner:   &exec.OSRunner{},
		Log: func(level, msg string) {
			if level == "warn" || level == "error" {
				fmt.Fprintf(os.Stderr, "halite-node: %s: %s\n", level, msg)
			}
		},
	}
}

// dispatcher lets a module call another module.
type dispatcher struct{ r *exec.Registry }

func (d dispatcher) Call(c *exec.Context, name string, args *value.Map) (any, error) {
	return d.r.Call(c, name, args)
}

func (d dispatcher) Has(name string) bool { return d.r.Has(name) }

// CallPositional is the optional half of the dispatcher interface, which
// a rendered file template needs so that `salt['pillar.get']('a:b')`
// binds its argument by position as Salt does.
func (d dispatcher) CallPositional(c *exec.Context, name string, args []any, kwargs *value.Map) (any, error) {
	return d.r.CallPositional(c, name, args, kwargs)
}

// newJobID is the operator-readable, monotonic form of SPEC section 6.3.
func newJobID() string { return time.Now().UTC().Format("20060102T150405.000000") }

func (n *node) out(v any) {
	if err := cli.Write(os.Stdout, v, n.format, n.indent); err != nil {
		cli.Fatalf("%v", err)
	}
}
