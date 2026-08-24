package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/keystore"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

// hubContext is what every hub subcommand that touches key material
// needs: the configuration, the CA, the key store, and a logger.
type hubContext struct {
	cfg   *config.Config
	log   *hlog.Logger
	files pki.Files
	store *keystore.Store
	auth  *keystore.Authority
	// denied is this process's handshake denylist. For an operator
	// command it is nowhere near a handshake, and the running hub picks
	// the decision up from the store; see hub.Server.Reconcile.
	denied *transport.Denylist
}

// openHub loads configuration and key material. create says whether an
// absent enrollment CA is an error or something to make.
func openHub(args *cli.Args, create bool) *hubContext {
	cfg, err := config.Load(config.Hub, config.LoadOptions{
		Path:         args.Flag("config", ""),
		Root:         args.Flag("root", config.DefaultRoot),
		AllowMissing: true,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	secrets := redact.New()
	cli.Redact = secrets.Scrub
	logger, err := buildLogger(args, cfg, secrets)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	for _, w := range cfg.Warnings {
		logger.Warn(w, "component", "config")
	}

	files := pki.Files{Dir: args.Flag("pki-dir", cfg.String("pki_dir", config.DefaultPKIDir))}
	alg, err := pki.ParseKeyAlgorithm(cfg.String("key_algorithm", string(pki.ECDSAP256)))
	if err != nil {
		cli.Fatalf("%v", err)
	}

	var ca *pki.CA
	switch {
	case files.Exists(pki.CACertFile):
		ca, err = files.LoadCA(nil)
	case create:
		// The CA is created once, and every node in the estate will be
		// issued by it, so this is said out loud rather than logged at
		// debug and forgotten.
		ca, err = files.CreateCA(alg, "halite enrollment CA", 10*365*24*time.Hour)
		if err == nil {
			fingerprint, _ := pki.FingerprintCert(ca.Cert)
			logger.Warn("created a new enrollment CA",
				"dir", files.Dir, "algorithm", string(alg), "fingerprint", fingerprint)
		}
	default:
		cli.Fatalf("there is no enrollment CA in %s; `halite-hub serve` creates one on first run", files.Dir)
	}
	if err != nil {
		cli.Fatalf("%v", err)
	}

	store, err := keystore.Open(keysDir(cfg))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	mode, err := keystore.ParseMode(cfg.String("enrollment_mode", string(keystore.ModeManual)))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	denied := transport.NewDenylist()
	return &hubContext{
		cfg:    cfg,
		log:    logger,
		files:  files,
		store:  store,
		denied: denied,
		auth: &keystore.Authority{
			Store:    store,
			CA:       ca,
			Mode:     mode,
			Lifetime: cfg.Duration("certificate_lifetime", keystore.DefaultLifetime),
			Revoker:  denied,
		},
	}
}

// keysDir is where the enrollment records live: durable state, not
// cache, because losing it means the fleet enrols again.
func keysDir(cfg *config.Config) string {
	return filepath.Join(cfg.String("state_dir", config.DefaultStateDir), "keys")
}

// buildLogger reads SPEC 26.1's settings, by the names the settings
// actually have: a first cut here read `log_fmt`, which is not one, and
// the loader said so on every start.
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
		Fields:  map[string]any{"component": "hub"},
		Secrets: secrets,
	})
}

// runServe is the control plane of SPEC section 6.
func runServe(args *cli.Args) int {
	h := openHub(args, true)
	listen := args.Flag("listen", h.cfg.String("listen", fmt.Sprintf(":%d", transport.DefaultPort)))

	pair, err := servingCertificate(h, args, listen)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	n, err := h.auth.LoadDenylist()
	if err != nil {
		cli.Fatalf("%v", err)
	}

	jobs, err := job.OpenCache(filepath.Join(h.cfg.String("state_dir", config.DefaultStateDir), "jobs"))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	// SPEC 9.4: retention is by age and by size, whichever binds first,
	// and the hub enforces it rather than an external cron job.
	jobs.Retention = h.cfg.Duration("job_cache_retention", 30*24*time.Hour)
	jobs.MaxBytes = h.cfg.Int("job_cache_max_size", 10<<30)

	nodes, err := hub.OpenNodeCache(filepath.Join(h.cfg.String("cache_dir", config.DefaultCacheDir), "nodes"))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	nodes.StaleAfter = h.cfg.Duration("grain_stale_after", time.Hour)

	// SPEC 19.5's mine: what nodes publish for other nodes to read.
	mineStore, err := hub.OpenMineStore(
		filepath.Join(h.cfg.String("state_dir", config.DefaultStateDir), "mine"))
	if err != nil {
		cli.Fatalf("%v", err)
	}

	// SPEC 19.1 keeps an orchestration as a first-class object with its
	// own timeline, which is what makes `orch show` and `orch resume`
	// possible. It is durable state, beside the job cache.
	orchestrations, err := hub.OpenOrchStore(
		filepath.Join(h.cfg.String("state_dir", config.DefaultStateDir), "orch"))
	if err != nil {
		cli.Fatalf("%v", err)
	}

	groups, err := nodegroupsFrom(h.cfg)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	// SPEC 18.1's reactor. A configuration that will not parse stops
	// the hub rather than starting one with a reactor that reacts to
	// nothing, which is indistinguishable from a quiet estate.
	reactorRaw, _ := h.cfg.Get("reactor")
	reactors, err := hub.ParseReactors(reactorRaw)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	// The tree this hub serves. SPEC 13.5's two hiding settings and the
	// symlink policy live on it, and they were all three declared and
	// read by nothing until there was a file server to read them.
	var files *fileserver.Roots
	if roots := h.cfg.Roots("file_roots"); len(roots) > 0 {
		if err := checkRootsAreNotTheHubsOwn(h, roots); err != nil {
			cli.Fatalf("%v", err)
		}
		files = fileserver.NewRoots(roots)
		files.FollowSymlinks = h.cfg.Bool("fileserver_follow_symlinks", false)
		files.IgnoreGlobs = h.cfg.StringSlice("file_ignore_glob")
		if err := files.SetIgnoreRegexes(h.cfg.StringSlice("file_ignore_regex")); err != nil {
			cli.Fatalf("%v", err)
		}
		if backends := h.cfg.StringSlice("fileserver_backend"); len(backends) > 0 {
			for _, b := range backends {
				if b != "roots" {
					h.log.Warn("this build serves only the roots backend",
						"configured", b, "section", "13.2")
				}
			}
		}
	}

	// Hub-side pillar. Without pillar_roots the hub compiles none and
	// says so to a node that asks, rather than answering with an empty
	// pillar that looks like a successful compilation of nothing.
	var pillarOpts *hub.PillarOptions
	if roots := h.cfg.Roots("pillar_roots"); len(roots) > 0 {
		if err := checkRootsAreNotTheHubsOwn(h, roots); err != nil {
			cli.Fatalf("%v", err)
		}
		strategy, ok := value.ParseStrategy(h.cfg.String("pillar_source_merging_strategy", "smart"))
		if !ok {
			cli.Fatalf("pillar_source_merging_strategy %q is not a strategy; try smart, recurse, aggregate, or overwrite",
				h.cfg.String("pillar_source_merging_strategy", ""))
		}
		undefined := template.Strict
		if h.cfg.String("undefined", "strict") == "permissive" {
			undefined = template.Permissive
		}
		pillarOpts = &hub.PillarOptions{
			Roots:            fileserver.NewRoots(roots),
			TrustedGrains:    h.cfg.StringSlice("pillar_trusted_grains"),
			Strategy:         strategy,
			MergeLists:       h.cfg.Bool("pillar_merge_lists", false),
			Undefined:        undefined,
			GPG:              gpgOptionsFor(h.cfg),
			Renderer:         strings.Split(h.cfg.String("renderer", "jinja|yaml"), "|"),
			YAMLBool11:       h.cfg.OptionalBool("yaml_bool_11"),
			Nondeterministic: h.cfg.String("random_seed", "deterministic") == "nondeterministic",
			Registry:         builtin.New().Exec,
			ConfigValues:     h.cfg.Redacted(),
		}
		if ext := h.cfg.StringSlice("ext_pillar"); len(ext) > 0 {
			h.log.Warn("external pillar is not built; these sources contribute nothing",
				"sources", strings.Join(ext, ","), "section", "12.7")
		}
	}

	loaded, err := loadPolicy(h)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	// The event bus of SPEC 17. Durable, so that a reactor restart is
	// lossless and an incident can be reconstructed -- which is the
	// property Salt's in-memory bus does not have.
	bus, err := eventbus.Open(filepath.Join(h.cfg.String("state_dir", config.DefaultStateDir), "events"))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	bus.Retention = h.cfg.Duration("event_retention", eventbus.DefaultRetention)
	bus.MaxBytes = h.cfg.Int("event_max_size", eventbus.DefaultMaxBytes)
	defer bus.Close()

	server := &hub.Server{
		Authority:      h.auth,
		Log:            h.log,
		PingInterval:   h.cfg.Duration("hub_alive_interval", 30*time.Second),
		Jobs:           jobs,
		Nodes:          nodes,
		Orch:           orchestrations,
		Mine:           mineStore,
		Reactors:       reactors,
		Nodegroups:     groups,
		Files:          files,
		HashType:       h.cfg.String("hash_type", "sha256"),
		Pillar:         pillarOpts,
		Policy:         loaded,
		Events:         bus,
		EventTagCompat: h.cfg.Bool("event_tag_compat", false),
	}

	ln, err := hub.Listen(listen, pair, h.auth.CA.Cert, h.denied)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	envs := "none"
	if files != nil {
		envs = strings.Join(files.Envs(), ",")
	}
	fingerprint, _ := pki.FingerprintCert(h.auth.CA.Cert)
	h.log.Info("hub listening",
		"file_roots", envs,
		"address", ln.Addr().String(),
		"version", version.String(),
		"enrollment_mode", string(h.auth.Mode),
		"ca_fingerprint", fingerprint,
		"revocations_loaded", n,
		"keys", h.store.Dir(),
		"events", bus.Dir())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A batch in flight belongs to the hub, so stopping the hub stops
	// it -- rather than leaving a goroutine delivering into a store
	// that is being shut down. `jobs resume` picks it up afterwards.
	server.Context = ctx
	// The operator command line is a separate process, so the running
	// hub follows the key store rather than being told.
	go server.Reconcile(ctx, 2*time.Second)
	if len(reactors) > 0 {
		reactor := &hub.Reactor{
			Server:     server,
			Entries:    reactors,
			Workers:    int(h.cfg.Int("reactor_workers", 0)),
			QueueDepth: int(h.cfg.Int("reactor_queue_depth", 0)),
			MaxDepth:   int(h.cfg.Int("max_causality_depth", 0)),
			Timeout:    h.cfg.Duration("reactor_timeout", 0),
			// Where it had read to. SPEC 17.2 makes a reactor restart
			// lossless, and it is lossless because this file exists.
			OffsetFile: filepath.Join(h.cfg.String("state_dir", config.DefaultStateDir), "reactor.offset"),
		}
		h.log.Info("reactor started",
			"entries", len(reactors), "tags", strings.Join(hub.ReactorTags(reactors), ","))
		go func() {
			if err := reactor.Run(ctx); err != nil {
				h.log.Error("the reactor stopped", "error", err.Error())
			}
		}()
	}
	go maintain(ctx, h, server, jobs, bus)

	if err := server.Serve(ctx, ln); err != nil {
		h.log.Error("the hub stopped", "error", err.Error())
		return 1
	}
	h.log.Info("the hub stopped")
	return 0
}

// loadPolicy reads the RBAC file of SPEC 23.5.
//
// A missing file is not an error and is not a grant: the hub starts,
// says so, and authorizes nothing until one exists. Deny by default has
// to mean that when the file is absent as well as when it is empty, or
// it means nothing at all.
func loadPolicy(h *hubContext) (*policy.Policy, error) {
	path := h.cfg.String("policy", filepath.Join(config.DefaultRoot, "policy.yaml"))
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		h.log.Warn("there is no policy file, so no operator may submit a job",
			"path", path, "fix", "halite-hub keys operator create <name> --admin", "section", "23.5")
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the policy at %s: %w", path, err)
	}
	loaded, warnings, err := policy.Load(src, path)
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		h.log.Warn(w.String(), "component", "policy", "path", path)
	}
	loaded.ArbitraryCode = arbitraryCodeFunctions()
	h.log.Info("policy loaded",
		"path", path, "roles", len(loaded.Roles), "bindings", len(loaded.Bindings),
		"never_granted_by_wildcard", len(loaded.ArbitraryCode))
	return loaded, nil
}

// arbitraryCodeFunctions is the set a wildcard never grants, taken from
// the signatures this build ships rather than from a list here: a
// function marked `arbitrary_code` in a later build is covered without
// anyone remembering.
func arbitraryCodeFunctions() map[string]bool {
	out := map[string]bool{}
	registries := builtin.New()
	// Both registries: SPEC 23.5 names `module.run`, which is a state
	// module rather than an execution one. It is not submittable in
	// this build, and listing it costs nothing and covers the build
	// where it is.
	for _, r := range []*signature.Registry{registries.Exec.Signatures(), registries.States.Signatures()} {
		for _, name := range r.Names() {
			if sig, ok := r.Lookup(name); ok && sig.ArbitraryCode {
				out[name] = true
			}
		}
	}
	return out
}

// gpgOptionsFor is SPEC 12.6's renderer settings, which the hub needs
// because an encrypted pillar file is decrypted where it is compiled.
func gpgOptionsFor(cfg *config.Config) render.GPGOptions {
	return render.GPGOptions{
		Binary:  cfg.String("gpg_binary", ""),
		Home:    cfg.String("gpg_home", ""),
		Timeout: cfg.Duration("gpg_timeout", 30*time.Second),
	}
}

// checkRootsAreNotTheHubsOwn refuses a file root that holds the hub's
// own state, cache, or key material.
//
// This is not hypothetical. Setting `file_roots: /srv/halite` beside
// `state_dir: /srv/halite/state` is an easy thing to write, and it
// makes the hub serve its key store and its job cache -- every return
// in the estate, which is where pillar-derived values end up -- to
// every enrolled node. It happened in this project's own lab. A
// refusal at startup is the only place to catch it, because everything
// afterwards looks like it is working.
func checkRootsAreNotTheHubsOwn(h *hubContext, roots map[string][]string) error {
	private := map[string]string{
		"the key material": h.files.Dir,
		"the key store":    h.store.Dir(),
		"durable state":    h.cfg.String("state_dir", config.DefaultStateDir),
		"the cache":        h.cfg.String("cache_dir", config.DefaultCacheDir),
	}
	for env, dirs := range roots {
		for _, dir := range dirs {
			served, err := filepath.Abs(dir)
			if err != nil {
				return fmt.Errorf("file root %q: %w", dir, err)
			}
			for what, path := range private {
				if path == "" {
					continue
				}
				mine, err := filepath.Abs(path)
				if err != nil {
					continue
				}
				if contains(served, mine) || contains(mine, served) {
					return fmt.Errorf(
						"file_roots for %q serves %s, which holds %s (%s): "+
							"every enrolled node could read it. Move one of the two.",
						env, served, what, mine)
				}
			}
		}
	}
	return nil
}

// contains reports whether inner is outer or is beneath it.
func contains(outer, inner string) bool {
	if outer == inner {
		return true
	}
	rel, err := filepath.Rel(outer, inner)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// nodegroupsFrom reads the nodegroup table, checked at load rather than
// at use: a cycle or a reference to a group that does not exist is a
// configuration error, and finding it when a job is dispatched means
// finding it at the worst moment.
func nodegroupsFrom(cfg *config.Config) (target.Nodegroups, error) {
	m := cfg.Map("nodegroups")
	if m == nil || m.Len() == 0 {
		return nil, nil
	}
	groups := target.Nodegroups{}
	for _, e := range m.Entries() {
		name, ok := e.Key.(string)
		if !ok {
			return nil, fmt.Errorf("a nodegroup name must be a string, and %v is not", e.Key)
		}
		expr, ok := e.Val.(string)
		if !ok {
			return nil, fmt.Errorf("nodegroup %q must be a target expression, and %v is not", name, e.Val)
		}
		groups[name] = expr
	}
	if err := target.ValidateNodegroups(groups); err != nil {
		return nil, err
	}
	return groups, nil
}

// maintain does the hub's two housekeeping jobs: settling jobs whose
// window has passed, so that "what is running" has an answer, and
// enforcing the job cache's retention, so that a hub left running does
// not fill its disk. Salt's local_cache does. // lexicon:allow
func maintain(ctx context.Context, h *hubContext, server *hub.Server, jobs *job.Cache, bus *eventbus.Bus) {
	settle := time.NewTicker(time.Minute)
	defer settle.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	// The `fsync: interval` class of SPEC 17.2. The security-relevant
	// tags are already durable when Append returns; this is the rest.
	sync := time.NewTicker(5 * time.Second)
	defer sync.Stop()

	sweep := func() {
		if removed, err := jobs.Prune(); err != nil {
			h.log.Warn("pruning the job cache", "error", err.Error())
		} else if removed > 0 {
			h.log.Info("pruned the job cache", "removed", removed)
		}
		if removed, err := bus.Prune(); err != nil {
			h.log.Warn("pruning the event bus", "error", err.Error())
		} else if removed > 0 {
			h.log.Info("pruned the event bus", "segments", removed)
		}
	}
	sweep()
	for {
		select {
		case <-ctx.Done():
			return
		case <-settle.C:
			if _, err := server.Settle(); err != nil {
				h.log.Warn("settling finished jobs", "error", err.Error())
			}
		case <-sync.C:
			if err := bus.Sync(); err != nil {
				h.log.Warn("syncing the event bus", "error", err.Error())
			}
		case <-prune.C:
			sweep()
		}
	}
}

// servingCertificate loads the hub's own certificate, issuing one if
// there is none or if the one on disk has expired.
//
// The hub's serving key and the CA key are separate, per SPEC 7.5.
func servingCertificate(h *hubContext, args *cli.Args, listen string) (tls.Certificate, error) {
	names := serverNames(args, listen)
	if h.files.Exists(pki.HubCertFile) && h.files.Exists(pki.HubKeyFile) {
		cert, err := h.files.ReadCert(pki.HubCertFile)
		if err != nil {
			return tls.Certificate{}, err
		}
		if time.Now().Before(cert.NotAfter) {
			return h.files.KeyPair(pki.HubCertFile, pki.HubKeyFile)
		}
		h.log.Warn("the hub's certificate has expired; issuing another",
			"not_after", cert.NotAfter.UTC().Format(time.RFC3339))
	}

	key, err := h.files.ReadKey(pki.HubKeyFile)
	if err != nil {
		alg, _ := pki.ParseKeyAlgorithm(h.cfg.String("key_algorithm", string(pki.ECDSAP256)))
		key, err = pki.GenerateKey(alg)
		if err != nil {
			return tls.Certificate{}, err
		}
		if err := h.files.WriteKey(pki.HubKeyFile, key); err != nil {
			return tls.Certificate{}, err
		}
	}
	der, err := h.auth.CA.IssueHub(key, names, h.cfg.Duration("certificate_lifetime", keystore.DefaultLifetime))
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := h.files.WriteCert(pki.HubCertFile, der); err != nil {
		return tls.Certificate{}, err
	}
	h.log.Info("issued the hub's serving certificate", "names", strings.Join(names, ","))
	return h.files.KeyPair(pki.HubCertFile, pki.HubKeyFile)
}

// serverNames is what a node may dial this hub by. A name missing from
// here is a handshake failure at the node, so the default is generous
// about the local machine and the operator adds the rest.
func serverNames(args *cli.Args, listen string) []string {
	if v := args.Flag("names", ""); v != "" && v != "true" {
		return strings.Split(v, ",")
	}
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	add("localhost")
	add("127.0.0.1")
	add("::1")
	if host, err := os.Hostname(); err == nil {
		add(host)
		if fqdn, err := net.LookupCNAME(host); err == nil {
			add(strings.TrimSuffix(fqdn, "."))
		}
	}
	if host, _, err := net.SplitHostPort(listen); err == nil && host != "0.0.0.0" && host != "::" {
		add(host)
	}
	return names
}
