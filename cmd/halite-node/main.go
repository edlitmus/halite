// Command halite-node is the endpoint agent and the local executor. It
// replaces three of Salt's entry points in one binary: the agent, the
// local caller, and the proxy. SPEC section 2.2.
//
// This build carries everything a node does about its own tree without a
// hub, and everything it does with one: enrollment, renewal, the
// subscribe stream, running the jobs that arrive on it, hub-compiled
// pillar, the file server as a source, beacons, the scheduler, and the
// mine.
package main

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/job"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/returner"
	"github.com/edlitmus/halite/internal/state"
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
  halite-node enroll                             ask a hub for a certificate
  halite-node renew                              replace this node's certificate
  halite-node connect                            hold the stream open to the hub
  halite-node event send <tag> [json]            put an event on the hub's bus

enroll and connect flags:
  --hub <address>      the hub to dial, default from the hub setting
  --ca-file <path>     the hub CA to pin, for a node enrolling for the first time
  --hub-fingerprint <fp>  the CA digest this node expects, checked before enrolling
  --token <secret>     a bootstrap token, for the token mode of SPEC 7.3
  --wait               keep asking until an operator accepts the request
  --pki-dir <dir>      key material, default ` + config.DefaultPKIDir + `
  --force              enrol again from a new key, moving the old one aside
  --key-algorithm <a>  ecdsa-p256 (default) or ecdsa-p384
  --server-name <name> the name to verify in the hub's certificate
  --correlation <id>   tie an event to the ones that caused it

Common flags:
  --help               describe the program without running a command
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

	// `--help` after any subcommand describes the program rather than
	// running it: `enroll --help` must not enrol.
	if args.Bool("help", false) {
		fmt.Print(usage)
		os.Exit(0)
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
	case "enroll":
		os.Exit(runEnroll(args))
	case "renew":
		os.Exit(runRenew(args))
	case "connect", "serve":
		os.Exit(runConnect(args))
	case "event":
		os.Exit(runEvent(args))
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
	// files is the tree a run compiles against: this node's own roots,
	// or the hub's file server. One interface, because a state that
	// says `source: salt://web/nginx.conf` must not know which it got.
	files   stateTree
	pillars *fileserver.Roots
	undef   template.UndefinedMode
	// args and root are kept so that a job from a hub naming a
	// different environment can have its roots rebuilt, rather than
	// silently running against the environment this invocation happened
	// to be started in.
	args *cli.Args
	root string
	// executor and refusals exist only in the agent: a one-shot command
	// line has no queue and nothing to post a refusal to.
	executor *executor
	refusals chan *job.Return
	// hubPillar asks the hub to compile this node's pillar. Nil for a
	// node working from its own roots, which is what `--local` and a
	// node with no hub do.
	hubPillar func(env string) (*value.Map, error)
	// events forwards an event to the hub's bus. Nil for a node with
	// no hub, and `event.send` says so rather than reporting a success
	// nobody received.
	events exec.EventSender
	// mine publishes to and reads from the mine on the hub. Nil for a
	// node with no hub, where there is nothing to publish to.
	mine exec.MineAccess
	// returns is the configured returner of SPEC 20.3, built once at
	// startup so a misconfiguration stops the node rather than being
	// discovered by the first scheduled job that had somewhere to go.
	returns returner.Returner
	// extensions are the signed bridges of SPEC section 24. Nil on a
	// node with none, which is the normal case.
	extensions *extension.Runtime
	// hubTree records that this node is compiling against the hub's
	// tree, so a reconnect does not probe the file server again for a
	// tree it already has.
	hubTree bool
	// beacons and schedule are the running engines, which the
	// management functions of SPEC 16.1 and 20.1 act on. Nil on a
	// one-shot command line, where there is nothing running to change.
	beacons  exec.BeaconControl
	schedule exec.ScheduleControl
	// reloadSchedule re-reads the schedule from disk. Set with the
	// engine.
	reloadSchedule func() error
	// statesRunning counts the state runs in progress, which is what
	// `disable_during_state_run` reads. SPEC 16.3.
	//
	// A pointer, because a job runs against a shallow copy of the node
	// and the two have to agree about whether a state run is in
	// progress -- and because a lock may not be copied at all.
	statesRunning *atomic.Int64
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
		statesRunning: new(atomic.Int64),
		cfg:           cfg,
		log:           logger,
		secrets:       secrets,
		registry:      builtin.New(),
		env:           args.Flag("env", cfg.String("env", "base")),
		test:          args.Bool("test", cfg.Bool("test", false)),
		format:        format,
		indent:        int(parseInt(args.Flag("indent", "0"))),
		undef:         template.Strict,
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

	// The signed extensions of SPEC section 24, loaded before anything
	// can call one. A failure here is a warning: an extension that does
	// not verify must not run, and must not stop the node either — a
	// node that refuses to start cannot be sent the highstate that
	// would fix it.
	n.openExtensions()

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
	n.args = args
	n.root = root
	n.refusals = make(chan *job.Return, 16)
	n.files = fileserver.NewFetcher(fileserver.NewRoots(n.fileRootsFor(n.env)))
	n.pillars = fileserver.NewRoots(n.pillarRootsFor(n.pillarEnv))
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

// tree is what a state run reads from: the compiler's view of it and a
// module's view of it, which are the same tree seen two ways.
//
// Defined on the consumer, as an interface, so that the hub's file
// server and the node's own roots are interchangeable at the one place
// that chooses between them.
type stateTree interface {
	state.Loader
	exec.FileFetcher
}

// fileRootsFor and pillarRootsFor are the roots for one environment.
func (n *node) fileRootsFor(env string) map[string][]string {
	return rootsFrom(n.args, n.cfg, "file-root", "file_roots",
		[]string{filepath.Join(n.root, "state"), "/srv/halite/states", "/srv/salt"}, env)
}

func (n *node) pillarRootsFor(env string) map[string][]string {
	return rootsFrom(n.args, n.cfg, "pillar-root", "pillar_roots",
		[]string{filepath.Join(n.root, "pillar"), "/srv/halite/pillar", "/srv/pillar"}, env)
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
func (n *node) compilePillarOrErr() (*value.Map, error) {
	// A node with a hub gets its pillar from the hub: SPEC 12.1 puts
	// the compilation there so that the node holds none of it and
	// cannot read another node's.
	if n.hubPillar != nil {
		return n.hubPillar(n.pillarEnv)
	}
	strategy, _ := value.ParseStrategy(n.cfg.String("pillar_source_merging_strategy", "smart"))
	c := &pillar.Compiler{
		Loader: n.pillars,
		Config: pillar.Config{
			// `salt['pillar.get']` inside a pillar file sees the pillar
			// built so far, so the dispatcher is built per render.
			NewSalt: func(partial *value.Map) template.Dispatcher {
				return exec.TemplateDispatcher{Registry: n.registry.Exec, Context: n.pillarContext(partial)}
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
	return out.Pillar, out.Err()
}

// compilePillar is the command-line form: a pillar that will not
// compile ends the invocation. The agent uses the form above, because a
// job that cannot be run has to be returned as a failure rather than
// take the process with it.
func (n *node) compilePillar() *value.Map {
	p, err := n.compilePillarOrErr()
	if err != nil {
		cli.Fatalf("%v", err)
	}
	return p
}

// context builds the module execution context for a local run, which
// assigns its own job identifier.
func (n *node) context(p *value.Map) *exec.Context {
	return n.contextFor(p, newJobID())
}

// pillarContext is the context a pillar file renders under.
//
// It is the run context with `pillar.refresh` taken away: the hook
// recompiles the pillar, and a pillar template that reached it would
// recompile the pillar it is in the middle of compiling.
func (n *node) pillarContext(partial *value.Map) *exec.Context {
	c := n.context(partial)
	c.RecompilePillar = nil
	return c
}

// contextFor is the same with the identifier supplied, so that a job
// driven from a hub carries the hub's jid all the way down: a module
// that logs one, and the return that is filed under it, name the same
// job.
func (n *node) contextFor(p *value.Map, jobID string) *exec.Context {
	return &exec.Context{
		Ctx:          context.Background(),
		Grains:       n.grains,
		Pillar:       p,
		Config:       n.cfg.Redacted(),
		NodeID:       n.nodeID,
		Env:          n.env,
		JobID:        jobID,
		Test:         n.test,
		Files:        n.files,
		Dispatch:     dispatcher{n.registry.Exec},
		Events:       n.events,
		Mine:         n.mine,
		Beacons:      n.beacons,
		Schedule:     n.schedule,
		SaveConfig:   n.saveRuntimeConfig,
		ReloadConfig: n.reloadRuntimeConfig,
		Runner:       &exec.OSRunner{},
		// `pillar.refresh` rebuilds through the same path a run uses,
		// so a node on a hub asks the hub and a local one recompiles
		// its roots, without the module knowing which it is.
		RecompilePillar: n.compilePillarOrErr,
		Extensions:      n.describeExtensions,
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

// newJobID is the identifier a local run files itself under. The format
// is shared with the hub's, so a jid means the same thing whichever end
// assigned it. SPEC section 6.3.
func newJobID() string { return string(job.NewID(time.Now())) }

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
