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
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/grains"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/redact"
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
  --pillarenv <name>   pillar environment, defaulting to --env
  --id <node-id>       override the node identity
  --out <format>       nested (default), json, yaml, txt, quiet
  --indent <n>         indent for json output
  --test               run every state in test mode, changing nothing
  --permissive         allow undefined template names, as Salt does
  --log-level <level>  error, warn, info (default), debug, or trace
  --log-fmt <format>   json (default) or console
  --legacy-arg-parse   read every argument as YAML, as Salt does, and log
                       each coercion; SPEC section 9.2

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
	// pillarEnv is the environment the pillar is compiled from, which
	// defaults to env but need not equal it: SPEC 12.2 lets a tree hold
	// its pillar in one environment while states move between several.
	pillarEnv string
	test      bool
	format    cli.Format
	indent    int
	log       *hlog.Logger
	secrets   *redact.Set
	files     *fileserver.Roots
	pillars   *fileserver.Roots
	undef     template.UndefinedMode
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
	// The redactor is built before the logger, because the logger holds
	// it: SPEC 26.1 applies redaction at the sink so that a log line
	// added later cannot forget about it.
	secrets := redact.New()
	seedConfiguredSecrets(secrets, cfg)
	logger, err := buildLogger(args, cfg, secrets)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	cli.Redact = secrets.Scrub
	for _, w := range cfg.Warnings {
		logger.Warn(w, "component", "config")
	}

	format, err := cli.ParseFormat(args.Flag("out", cfg.String("output", "nested")))
	if err != nil {
		cli.Fatalf("%v", err)
	}

	n := &node{
		cfg:      cfg,
		log:      logger,
		secrets:  secrets,
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

	// SPEC 28.3's environment controls, the pair to state_allowlist and
	// state_denylist. They were declared, documented, and enforced by
	// nothing, so a node restricted to `base` would happily apply
	// anything a `--env` said. A control that does not control is worse
	// than an absent one, because someone is relying on it.
	if err := checkEnvPermitted(cfg, n.env); err != nil {
		cli.Fatalf("%v", err)
	}
	// SPEC 12.2's `pillarenv`, which was declared and read by nothing:
	// a tree holding its pillar in one environment while its states
	// moved between several got the states' environment for both.
	n.pillarEnv = args.Flag("pillarenv", cfg.String("pillarenv", n.env))
	if err := checkEnvPermitted(cfg, n.pillarEnv); err != nil {
		cli.Fatalf("%v", err)
	}

	n.nodeID = resolveNodeID(args, cfg)
	// SPEC 26.1 puts the identity on every record. It cannot go on at
	// construction because the logger reports the problems found while
	// resolving it.
	n.log = n.log.With("node_id", n.nodeID)

	g, warnings := grains.Collect(grains.Options{
		NodeID:     n.nodeID,
		StaticFile: root + "/grains",
		GrainsDir:  root + "/grains.d",
		Extra:      cfg.Map("grains"),
		Cloud:      cfg.Bool("cloud_grains", false),
	})
	for _, w := range warnings {
		n.log.Warn(w.String(), "component", "grains")
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
		[]string{filepath.Join(root, "pillar"), "/srv/halite/pillar", "/srv/pillar"}, n.pillarEnv))
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
	// The modifiers apply to a detected identity and not to one the
	// operator wrote down: an explicit `node_id` is the answer, not a
	// draft of it. Salt draws the line in the same place.
	return applyNodeIDModifiers(cfg, host)
}

// applyNodeIDModifiers is Salt's `minion_id_lowercase` and
// `minion_id_remove_domain`, which the compatibility shim translates and
// nothing read. `remove_domain` takes true, meaning any domain, or a
// domain to strip if the identity ends in it.
func applyNodeIDModifiers(cfg *config.Config, id string) string {
	if v, ok := cfg.Get("node_id_remove_domain"); ok && v != nil {
		switch t := v.(type) {
		case bool:
			if t {
				if host, _, found := strings.Cut(id, "."); found {
					id = host
				}
			}
		case string:
			suffix := "." + t
			if len(id) > len(suffix) && strings.EqualFold(id[len(id)-len(suffix):], suffix) {
				id = id[:len(id)-len(suffix)]
			}
		}
	}
	if cfg.Bool("node_id_lowercase", false) {
		id = strings.ToLower(id)
	}
	return id
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

// templateOptions reads the renderer settings a tree may set. They were
// declared, documented, and read by nothing, so a tree asking for
// `trim_blocks` did not get it.
func (n *node) templateOptions() *template.Options {
	opts := template.DefaultOptions()
	opts.TrimBlocks = n.cfg.Bool("template_trim_blocks", opts.TrimBlocks)
	opts.LstripBlocks = n.cfg.Bool("template_lstrip_blocks", opts.LstripBlocks)
	return &opts
}

// defaultRenderer reads SPEC section 10's `renderer`, the pipeline a
// file without a shebang gets. Declared, documented, and read by
// nothing, so a tree asking for `jinja|json` got `jinja|yaml`.
func (n *node) defaultRenderer() []string {
	spec := n.cfg.String("renderer", "")
	if spec == "" {
		return nil
	}
	var stages []string
	for _, part := range strings.Split(spec, "|") {
		if p := strings.TrimSpace(part); p != "" {
			stages = append(stages, p)
		}
	}
	return stages
}

// gpgOptions reads the gpg renderer's settings of SPEC section 12.6.
func (n *node) gpgOptions() render.GPGOptions {
	timeout, err := time.ParseDuration(n.cfg.String("gpg_timeout", "30s"))
	if err != nil {
		n.log.Warn(fmt.Sprintf("gpg_timeout: %v; using 30s", err), "component", "render")
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
			Env:           n.pillarEnv,
			NodeID:        n.nodeID,
			Grains:        n.grains,
			ConfigValues:  n.cfg.Redacted(),
			TrustedGrains: n.cfg.StringSlice("pillar_trusted_grains"),
			Strategy:      strategy,
			MergeLists:    n.cfg.Bool("pillar_merge_lists", false),
			Undefined:     n.undef,
			GPG:           n.gpgOptions(),
			OnSecret:      n.secrets.Add,
			Renderer:      n.defaultRenderer(),
			// Both are switches SPEC names and nothing read: 10.1.3's
			// `yaml_bool_11: false` for a tree that has been audited,
			// and 10.2.4's `random_seed: nondeterministic`.
			YAMLBool11:       n.cfg.OptionalBool("yaml_bool_11"),
			Nondeterministic: n.cfg.String("random_seed", "deterministic") == "nondeterministic",
			TemplateOptions:  n.templateOptions(),
			Local:            true,
		},
	}
	out := c.Compile()
	for _, w := range out.Warnings {
		n.log.Warn(w.String(), "component", "pillar")
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
			// A module names its own level, so the threshold is the
			// logger's rather than a hard-coded pair.
			lv, _ := hlog.ParseLevel(level)
			n.log.Log(lv, msg, "component", "module")
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

// checkEnvPermitted applies env_allowlist and env_denylist.
//
// The denylist wins, and an empty allowlist means no restriction — the
// same shape as state_allowlist and state_denylist, because an operator
// who has learnt one should not have to learn the other.
func checkEnvPermitted(cfg *config.Config, env string) error {
	for _, pattern := range cfg.StringSlice("env_denylist") {
		if ok, err := path.Match(pattern, env); err == nil && ok {
			return fmt.Errorf("environment %q is excluded by env_denylist (%q). SPEC section 28.3", env, pattern)
		}
	}
	allow := cfg.StringSlice("env_allowlist")
	if len(allow) == 0 {
		return nil
	}
	for _, pattern := range allow {
		if ok, err := path.Match(pattern, env); err == nil && ok {
			return nil
		}
	}
	return fmt.Errorf("environment %q is not in env_allowlist (%s). SPEC section 28.3",
		env, strings.Join(allow, ", "))
}

// buildLogger reads SPEC section 26.1's settings. They were declared,
// documented, and consulted by nothing, so every diagnostic went to
// stderr at whatever level it happened to be and `log_level: error` on
// an unattended node changed nothing.
func buildLogger(args *cli.Args, cfg *config.Config, secrets *redact.Set) (*hlog.Logger, error) {
	levelName := args.Flag("log-level", cfg.String("log_level", "info"))
	level, ok := hlog.ParseLevel(levelName)
	if !ok {
		return nil, fmt.Errorf("log_level %q is not a level; try error, warn, info, debug, or trace", levelName)
	}
	formatName := args.Flag("log-fmt", cfg.String("log_format", "json"))
	format, ok := hlog.ParseFormat(formatName)
	if !ok {
		return nil, fmt.Errorf("log_format %q is not a format; try json or console", formatName)
	}
	return hlog.New(hlog.Options{
		Level:   level,
		Format:  format,
		File:    cfg.String("log_file", ""),
		Fields:  map[string]any{"component": "node"},
		Secrets: secrets,
	})
}

// seedConfiguredSecrets records the values of the settings whose names
// say they hold one. The key-name rule already exists for what a
// template sees as `opts`; this makes the same values unprintable.
func seedConfiguredSecrets(secrets *redact.Set, cfg *config.Config) {
	for _, k := range config.Keys {
		if !config.IsSecretKey(k.Name) {
			continue
		}
		if v, ok := cfg.Get(k.Name); ok {
			secrets.AddTree(v)
		}
	}
}
