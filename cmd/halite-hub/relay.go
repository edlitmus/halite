package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/relay"
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
	nodeID := h.cfg.String("node_id", "")
	if nodeID == "" {
		cli.Fatalf("a relay needs `node_id`: it is the identity the upstream sees as the " +
			"single connected client, and the principal its `relay.proxy` grant is written for")
	}

	// The fleet has to exist before the relay reads it: the relay
	// reports its subordinates upstream when it connects, which is
	// before any node has necessarily arrived to create it lazily.
	if server.Fleet == nil {
		server.Fleet = hub.NewFleet()
	}

	built, err := relay.New(relay.Options{
		Server:    server,
		Upstream:  relayUpstream(h, args, upstream),
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
	return built
}

// relayUpstream is the client a relay presents itself with.
//
// Its own certificate, as SPEC 23.1 requires: the permission set covers
// proxying for its subordinate nodes and nothing else, so a compromised
// relay is a relay rather than an operator. It enrols with its upstream
// the way any node does, so the certificate is the node certificate in
// its own PKI directory.
func relayUpstream(h *hubContext, args *cli.Args, upstream string) *transport.Client {
	dir := args.Flag("upstream-pki-dir", h.cfg.String("relay_pki_dir", ""))
	if dir == "" {
		cli.Fatalf("a relay needs `relay_pki_dir`: the key material it enrolled with its " +
			"upstream, which is separate from the CA it issues to its own nodes")
	}
	files := pki.Files{Dir: dir}
	certPath := args.Flag("upstream-cert", files.Path(pki.NodeCertFile))
	keyPath := args.Flag("upstream-key", files.Path(pki.NodeKeyFile))
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		cli.Fatalf("relay: this relay has no certificate for its upstream at %s; "+
			"enrol it with `halite-node enroll --pki-dir %s`: %v", certPath, dir, err)
	}
	ca, err := files.ReadCert(pki.CACertFile)
	if err != nil {
		cli.Fatalf("relay: the upstream CA at %s: %v", files.Path(pki.CACertFile), err)
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
		HubURL: url, CA: ca, Cert: &pair,
		ServerName: args.Flag("upstream-server-name", h.cfg.String("relay_server_name", "")),
		Timeout:    h.cfg.Duration("relay_timeout", 60*time.Second),
	}
}
