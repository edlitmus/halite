package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/certreload"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/relay"
	"github.com/edlitmus/halite/internal/renewal"
	"github.com/edlitmus/halite/internal/transport"
)

// startRelay runs this hub as a relay, if it is configured as one.
//
// SPEC 5.3: it accepts node connections and presents itself upstream as
// a single client that proxies jobs, returns, events, and file
// requests.
func startRelay(ctx context.Context, h *hubContext, args *cli.Args, server *hub.Server) *relay.Relay {
	if !args.Bool("relay", h.cfg.Bool("relay", false)) {
		return nil
	}
	upstream := args.Flag("upstream", h.cfg.String("relay_upstream", ""))
	if upstream == "" {
		cli.Fatalf("--relay needs --upstream: a relay with no upstream is a hub")
	}
	if !strings.Contains(upstream, ":") {
		upstream = fmt.Sprintf("%s:%d", upstream, h.cfg.Int("relay_upstream_port", transport.DefaultPort))
	}

	spoolDir := h.cfg.String("relay_spool_dir", "")
	if spoolDir == "" {
		spoolDir = filepath.Join(h.cfg.String("state_dir", config.DefaultStateDir), "relay-spool")
	}
	upstreamClient, identity := relayUpstream(h, args, upstream)
	// The relay's identity upstream is the one its certificate names,
	// which is what the upstream authenticates and what its `relay.proxy`
	// grant is written for. It used to be read from `node_id`: the
	// relay refused to start without one, the operations guide said a
	// hub reads no `node_id`, and the configuration loader, which agrees
	// with the guide, warned on every start that the key "is not
	// recognised and was ignored" while this went on reading it. A relay
	// set up by the guide did not start, and one that did was told its
	// identity setting did nothing. DIVERGENCE 5.263.
	nodeID := identity.nodeID

	// The fleet has to exist before the relay reads it: the relay
	// reports its subordinates upstream when it connects, which is
	// before any node has necessarily arrived to create it lazily.
	if server.Fleet == nil {
		server.Fleet = hub.NewFleet()
	}

	built, err := relay.New(relay.Options{
		Server:    server,
		Upstream:  upstreamClient,
		ID:        nodeID,
		SpoolDir:  spoolDir,
		SpoolMax:  h.cfg.Int("relay_spool_max_size", relay.DefaultSpoolMax),
		EventTags: h.cfg.StringSlice("relay_event_tags"),
		Log: func(level, msg string, kv ...any) {
			if level == "warn" || level == "error" {
				h.log.Warn(msg, kv...)
				return
			}
			h.log.Info(msg, kv...)
		},
	})
	if err != nil {
		cli.Fatalf("relay: %v", err)
	}

	// Every return this hub files goes upstream, and the events its tag
	// globs name. Both through the relay, so a return reaches the
	// upstream by exactly the path it reached the local job cache.
	server.OnReturn = func(ret *job.Return) { built.Return(ret) }
	server.OnEvent = func(e *eventbus.Event) { built.ForwardEvent(e) }
	built.Register(server.Metrics)

	h.log.Info("relay starting",
		"relay", nodeID, "upstream", upstream,
		"event_tags", h.cfg.StringSlice("relay_event_tags"),
		"spool", spoolDir)

	go func() {
		if err := built.Run(ctx); err != nil {
			h.log.Error("the relay stopped", "error", err.Error())
		}
	}()
	go keepRelayRenewed(ctx, h, upstreamClient, identity)
	return built
}

// relayIdentity is the certificate a relay presents upstream, and where
// it lives.
type relayIdentity struct {
	files    pki.Files
	certPath string
	nodeID   string
	// renewable is false when --upstream-cert or --upstream-key put the
	// pair somewhere other than relay_pki_dir: a renewal writes to the
	// directory, and would replace a pair the relay is not presenting.
	renewable bool
}

// relayIdentityFrom reads the relay's identity from the certificate it
// presents upstream, and works out whether a renewal may replace it.
func relayIdentityFrom(files pki.Files, certPath, keyPath string) (relayIdentity, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return relayIdentity{}, fmt.Errorf("reading the upstream certificate at %s: %w", certPath, err)
	}
	leaf, err := pki.DecodeCert(certPEM)
	if err != nil {
		return relayIdentity{}, fmt.Errorf("the upstream certificate at %s: %w", certPath, err)
	}
	nodeID, err := pki.NodeIDFromCert(leaf)
	if err != nil {
		return relayIdentity{}, fmt.Errorf("the certificate at %s does not name a node, so it is "+
			"not one this relay enrolled upstream with: %w", certPath, err)
	}
	return relayIdentity{
		files:     files,
		certPath:  certPath,
		nodeID:    nodeID,
		renewable: certPath == files.Path(pki.NodeCertFile) && keyPath == files.Path(pki.NodeKeyFile),
	}, nil
}

// keepRelayRenewed renews the relay's upstream certificate at half its
// life, as `halite-node connect` renews a node's.
//
// Nothing did. A relay runs `halite-hub serve`, not `connect`, so its
// identity expired 90 days after it enrolled and the relay was refused
// upstream from then on, unless somebody ran `halite-node renew` for it
// (DIVERGENCE 5.263). The renewal is the node's own (internal/renewal):
// a new key, the upstream's certificate for it, both written to
// relay_pki_dir, the old key kept aside. The upstream revokes the old
// serial and ends the relay's stream; the client reads the new pair from
// disk on its next request (CertFiles, 5.254) and reconnects on it.
func keepRelayRenewed(ctx context.Context, h *hubContext, client *transport.Client, id relayIdentity) {
	if !id.renewable {
		h.log.Warn("this relay's upstream certificate is not renewed automatically, because "+
			"--upstream-cert or --upstream-key names a file outside relay_pki_dir; renew it there",
			"component", "relay", "cert", id.certPath)
		return
	}
	renewal.Loop(ctx, "this relay's upstream",
		func() (*x509.Certificate, error) { return id.files.ReadCert(pki.NodeCertFile) },
		func() (*x509.Certificate, error) {
			got, err := renewal.Identity(ctx, client, id.files, id.nodeID, "")
			if err != nil {
				return nil, err
			}
			h.log.Info("renewed this relay's upstream certificate", "component", "relay",
				"relay", id.nodeID, "expires", got.Cert.NotAfter.UTC().Format(time.RFC3339),
				"previous_key", got.Aside)
			if got.PruneErr != nil {
				h.log.Warn("could not remove a key an earlier renewal set aside",
					"component", "relay", "error", got.PruneErr.Error())
			}
			return got.Cert, nil
		},
		func(msg string, kv ...any) { h.log.Warn(msg, append(kv, "component", "relay")...) })
}

// relayUpstream is the client a relay presents itself with.
//
// Its own certificate, as SPEC 23.1 requires: the permission set covers
// proxying for its subordinate nodes and nothing else, so a compromised
// relay is a relay rather than an operator. It enrols with its upstream
// the way any node does, so the certificate is the node certificate in
// its own PKI directory.
func relayUpstream(h *hubContext, args *cli.Args, upstream string) (*transport.Client, relayIdentity) {
	dir := args.Flag("upstream-pki-dir", h.cfg.String("relay_pki_dir", ""))
	if dir == "" {
		cli.Fatalf("a relay needs `relay_pki_dir`: the key material it enrolled with its " +
			"upstream, which is separate from the CA it issues to its own nodes")
	}
	files := pki.Files{Dir: dir}
	certPath := args.Flag("upstream-cert", files.Path(pki.NodeCertFile))
	keyPath := args.Flag("upstream-key", files.Path(pki.NodeKeyFile))
	// Read from disk again before every request, not loaded once: the
	// certificate is renewed in place, by this relay (keepRelayRenewed)
	// or by `halite-node renew`, and the upstream revokes the old serial
	// as it issues the new one,
	// so a relay holding the pair it started with is refused from the
	// moment it is renewed until it is restarted. DIVERGENCE 5.254.
	certs, err := certreload.NewClient(certPath, keyPath,
		func(msg string, kv ...any) { h.log.Info(msg, append(kv, "component", "relay")...) },
		func(msg string, kv ...any) { h.log.Warn(msg, append(kv, "component", "relay")...) })
	if err != nil {
		cli.Fatalf("relay: this relay has no certificate for its upstream at %s; enrol it "+
			"with `halite-node enroll`, using a configuration whose pki_dir is %s "+
			"(docs/operations.md, \"Relays\"): %v", certPath, dir, err)
	}
	ca, err := files.ReadCert(pki.CACertFile)
	if err != nil {
		cli.Fatalf("relay: the upstream CA at %s: %v", files.Path(pki.CACertFile), err)
	}
	id, err := relayIdentityFrom(files, certPath, keyPath)
	if err != nil {
		cli.Fatalf("relay: %v", err)
	}

	url := upstream
	if !strings.Contains(upstream, "://") {
		if !strings.Contains(upstream, ":") {
			url = fmt.Sprintf("https://%s:%d", upstream, transport.DefaultPort)
		} else {
			url = "https://" + upstream
		}
	}
	return &transport.Client{
		HubURL: url, CA: ca, CertFiles: certs,
		ServerName: args.Flag("upstream-server-name", h.cfg.String("relay_server_name", "")),
		Timeout:    h.cfg.Duration("relay_timeout", 60*time.Second),
	}, id
}
