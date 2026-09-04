package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/edlitmus/halite/internal/account"
	"github.com/edlitmus/halite/internal/api"
	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/version"
)

// service is what `serve` assembles.
type service struct {
	cfg  *config.Config
	log  *hlog.Logger
	root string
}

// runServe starts the HTTP API of SPEC section 22.
func runServe(args *cli.Args) int {
	s := setup(args)

	tokens, err := apitoken.Open(
		filepath.Join(s.cfg.String("state_dir", config.DefaultStateDir), "tokens"))
	if err != nil {
		cli.Fatalf("%v", err)
	}

	accounts := loadAccounts(s, args)
	loaded := loadPolicy(s, args)
	hub := hubClient(s, args)

	// Named literally rather than through a helper, so the
	// declared-and-unread audit can see that something reads it: a key
	// nothing reads is a promise the configuration file makes and the
	// program does not keep.
	rawHooks, _ := s.cfg.Get("hooks")
	hooks, err := api.ParseHooks(rawHooks)
	if err != nil {
		// A hook configuration that will not parse stops the service
		// rather than starting one that serves some of the hooks and
		// 404s the rest.
		cli.Fatalf("%v", err)
	}

	server := &api.Server{
		Accounts:      accounts,
		Hooks:         api.NewHooks(hooks),
		Tokens:        tokens,
		Policy:        loaded,
		Hub:           hub,
		Signatures:    builtin.New().Exec.Signatures(),
		Log:           s.log,
		MaxBody:       s.cfg.Int("max_body", 64<<20),
		TokenLifetime: s.cfg.Duration("token_lifetime", 12*time.Hour),
		TokenIdle:     s.cfg.Duration("token_idle", 4*time.Hour),
		Metrics:       metricsRegistry(s.cfg),
		OIDC:          s.oidcProvider(),
		LDAP:          s.ldapClient(),
	}
	pair := servingCertificate(s, args)
	addr := args.Flag("listen", s.cfg.String("listen", ":4511"))
	ln, err := api.Listen(addr, pair)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	if locked := accounts.LockedOut(); len(locked) > 0 {
		// Before the first login rather than at it: these accounts are
		// configured with a TOTP this build cannot check, so they are
		// refused however correct the password is.
		s.log.Warn("these accounts require a second factor this build cannot check "+
			"and cannot log in; TOTP is HMAC-SHA-1 and this process is in FIPS mode (SPEC 27.4)",
			"accounts", locked)
	}

	s.log.Info("api listening",
		"address", ln.Addr().String(),
		"version", version.String(),
		"accounts", len(accounts.Names()),
		"hooks", len(hooks),
		"hub", hub.HubURL,
		"tokens", tokens.Dir())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Expired tokens are pruned on an interval rather than at every
	// read: the store is small, and a login should not pay for the
	// housekeeping of every token ever issued.
	go pruneTokens(ctx, s, tokens)

	srv := server.Serve(ln)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.log.Error("the api stopped", "error", err.Error())
		return 1
	}
	s.log.Info("the api stopped")
	return 0
}

// setup reads the configuration and opens the log.
func setup(args *cli.Args) *service {
	root := args.Flag("root", config.DefaultRoot)
	cfg, err := config.Load(config.API, config.LoadOptions{
		Path:         args.Flag("config", ""),
		Root:         root,
		AllowMissing: true,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintln(os.Stderr, w)
	}

	level, ok := hlog.ParseLevel(args.Flag("log-level", cfg.String("log_level", "info")))
	if !ok {
		cli.Fatalf("--log-level %q is not a level; try error, warn, info, debug, or trace",
			args.Flag("log-level", ""))
	}
	format := hlog.Console
	if args.Flag("log-fmt", cfg.String("log_fmt", "json")) == "json" {
		format = hlog.JSON
	}
	logger, err := hlog.New(hlog.Options{Level: level, Format: format})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	// A setting that parses and does nothing is indistinguishable from
	// one that works, until the thing it was meant to change does not
	// change. SPEC 4.1 accepts the key; saying so is what stops
	// acceptance reading as agreement.
	for _, w := range cfg.InertWarnings() {
		logger.Warn("this setting is accepted and does nothing",
			"setting", w.Setting, "effect", w.Effect, "section", w.Section)
	}

	return &service{cfg: cfg, log: logger, root: root}
}

// loadAccounts reads the local account file of SPEC 23.2.
//
// An absent file is an empty set rather than an error: an estate on
// OIDC alone has none, and refusing to start without one would make the
// break-glass path mandatory.
func loadAccounts(s *service, args *cli.Args) *account.File {
	path := args.Flag("accounts", s.cfg.String("accounts", filepath.Join(s.root, "accounts.yaml")))
	raw, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		s.log.Warn("no local account file; this service authenticates nobody locally", "path", path)
		return &account.File{Path: path, Accounts: map[string]*account.Account{}}
	}
	if err != nil {
		cli.Fatalf("%v", err)
	}
	f, err := account.Load(raw, path)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	return f
}

// loadPolicy reads the RBAC of SPEC 23.5.
func loadPolicy(s *service, args *cli.Args) *policy.Policy {
	path := args.Flag("policy", s.cfg.String("policy", config.DefaultPolicy))
	raw, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		// Deny by default, and said out loud: a service that
		// authorized nothing without explaining why would look broken.
		s.log.Warn("no policy file; this service authorizes nothing", "path", path)
		return nil
	}
	if err != nil {
		cli.Fatalf("%v", err)
	}
	loaded, warnings, err := policy.Load(raw, path)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	for _, w := range warnings {
		s.log.Warn(w.String(), "component", "policy")
	}
	return loaded
}

// hubClient is this service's own identity on the control plane.
//
// Its own operator certificate, not the hub's key material: the API is
// a client, and the whole point of the separation is that compromising
// it yields one certificate bounded by one policy rather than the
// control plane itself.
func hubClient(s *service, args *cli.Args) *transport.Client {
	files := pki.Files{Dir: args.Flag("pki-dir", s.cfg.String("pki_dir", config.DefaultPKIDir))}
	name := args.Flag("as", s.cfg.String("api_operator", "api"))

	certPath := args.Flag("cert", files.Path("operator-"+name+".crt"))
	keyPath := args.Flag("key", files.Path("operator-"+name+".key"))
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		cli.Fatalf("this service has no operator certificate at %s; "+
			"`halite-hub keys operator create %s` makes one: %v", certPath, name, err)
	}
	ca, err := files.ReadCert(pki.CACertFile)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	address := args.Flag("hub", s.cfg.String("hub", ""))
	if address == "" {
		cli.Fatalf("this service has no hub; set `hub` in the configuration or pass --hub")
	}
	url := address
	if !strings.Contains(address, "://") {
		if !strings.Contains(address, ":") {
			url = fmt.Sprintf("https://%s:%d", address, transport.DefaultPort)
		} else {
			url = "https://" + address
		}
	}
	return &transport.Client{
		HubURL:     url,
		CA:         ca,
		Cert:       &pair,
		ServerName: args.Flag("server-name", ""),
		Timeout:    30 * time.Second,
	}
}

// servingCertificate is what the API presents to its own clients.
func servingCertificate(s *service, args *cli.Args) tls.Certificate {
	certPath := args.Flag("tls-cert", s.cfg.String("tls_cert", ""))
	keyPath := args.Flag("tls-key", s.cfg.String("tls_key", ""))
	if certPath == "" || keyPath == "" {
		cli.Fatalf("this service needs a serving certificate; set `tls_cert` and `tls_key`, " +
			"or pass --tls-cert and --tls-key")
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	return pair
}

// pruneTokens drops expired records once they are past the retention
// an audit needs.
func pruneTokens(ctx context.Context, s *service, tokens *apitoken.Store) {
	keep := s.cfg.Duration("token_retention", 30*24*time.Hour)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if n, err := tokens.Prune(keep); err != nil {
			s.log.Warn("could not prune the token store", "error", err.Error())
		} else if n > 0 {
			s.log.Info("pruned expired tokens", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// metricsRegistry answers with a registry unless the operator turned
// metrics off. On by default, for the reason SPEC 26.2 gives: a
// backpressure design is auditable only if the counters are there
// before anyone needs them.
func metricsRegistry(cfg *config.Config) *metrics.Registry {
	if !cfg.Bool("metrics", true) {
		return nil
	}
	return metrics.NewRegistry()
}
