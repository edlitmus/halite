package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/keystore"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/transport"
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

	server := &hub.Server{
		Authority:    h.auth,
		Log:          h.log,
		PingInterval: h.cfg.Duration("hub_alive_interval", 30*time.Second),
	}

	ln, err := hub.Listen(listen, pair, h.auth.CA.Cert, h.denied)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	fingerprint, _ := pki.FingerprintCert(h.auth.CA.Cert)
	h.log.Info("hub listening",
		"address", ln.Addr().String(),
		"version", version.String(),
		"enrollment_mode", string(h.auth.Mode),
		"ca_fingerprint", fingerprint,
		"revocations_loaded", n,
		"keys", h.store.Dir())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The operator command line is a separate process, so the running
	// hub follows the key store rather than being told.
	go server.Reconcile(ctx, 2*time.Second)

	if err := server.Serve(ctx, ln); err != nil {
		h.log.Error("the hub stopped", "error", err.Error())
		return 1
	}
	h.log.Info("the hub stopped")
	return 0
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
