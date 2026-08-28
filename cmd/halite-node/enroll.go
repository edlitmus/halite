package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/job"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

// hubClient assembles the node's side of the transport from its
// configuration and whatever key material it already has.
func (n *node) hubClient(args *cli.Args) (*transport.Client, pki.Files) {
	files := pki.Files{Dir: args.Flag("pki-dir", n.cfg.String("pki_dir", config.DefaultPKIDir))}

	address := args.Flag("hub", n.cfg.String("hub", ""))
	if address == "" {
		cli.Fatalf("this node has no hub; set `hub` in %s or pass --hub", n.cfg.String("config_file", "the configuration"))
	}
	port := n.cfg.Int("hub_port", int64(transport.DefaultPort))
	url := address
	if !strings.Contains(address, "://") {
		if !strings.Contains(address, ":") {
			url = fmt.Sprintf("https://%s:%d", address, port)
		} else {
			url = "https://" + address
		}
	}

	client := &transport.Client{HubURL: url, ServerName: args.Flag("server-name", "")}

	// The CA is pinned at enrollment and read from disk afterwards. A
	// node with no CA on disk is enrolling for the first time, and
	// either the operator delivers one or this node fetches it and
	// checks it against the pinned fingerprint.
	caFile := args.Flag("ca-file", n.cfg.String("hub_ca_file", ""))
	want := args.Flag("hub-fingerprint", n.cfg.String("hub_fingerprint", ""))
	pinned := files.Exists(pki.CACertFile)

	// A CA this node has not already pinned is a CA it is being asked
	// to start trusting, and that decision needs the out-of-band
	// fingerprint whatever route the certificate arrived by. Only a CA
	// already on disk is exempt: it was checked when it was written.
	if !pinned && want == "" {
		cli.Fatalf("this node needs `hub_fingerprint` to enrol. Take it from "+
			"`halite-hub keys fingerprint` on the hub and set it in %s, or pass "+
			"--hub-fingerprint. It is what proves the CA is the right one, so there "+
			"is no enrolling without it",
			n.cfg.String("config_file", "the configuration"))
	}

	switch {
	case caFile != "":
		raw, err := os.ReadFile(caFile)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		cert, err := pki.DecodeCert(raw)
		if err != nil {
			cli.Fatalf("%s: %v", caFile, err)
		}
		client.CA = cert
	case pinned:
		cert, err := files.ReadCert(pki.CACertFile)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		client.CA = cert
	default:
		// No CA anywhere, and a fingerprint to check one against. The
		// hub presents its CA in the chain it serves; the fingerprint
		// decides whether to believe it, inside the handshake.
		cert, err := transport.FetchCA(context.Background(), client.HubURL, want,
			n.cfg.Duration("hub_timeout", 30*time.Second))
		if err != nil {
			cli.Fatalf("%v", err)
		}
		n.log.Info("fetched the hub CA and matched the pinned fingerprint",
			"hub", client.HubURL)
		client.CA = cert
	}

	// SPEC 7.3: the node checks the CA it was handed against a
	// fingerprint delivered by another route, so that a CA file
	// substituted in transit is caught here rather than never. The
	// fetched case has already been checked inside the handshake;
	// checking it again costs nothing and keeps the guarantee in one
	// readable place.
	if want != "" {
		got, err := pki.FingerprintCert(client.CA)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		if !pki.FingerprintEqual(want, got) {
			cli.Fatalf("the hub CA's fingerprint is %s and this node expects %s; refusing to enrol", got, want)
		}
		n.log.Info("the hub CA matches the pinned fingerprint", "fingerprint", got)
	}

	if files.Exists(pki.NodeCertFile) && files.Exists(pki.NodeKeyFile) {
		pair, err := files.KeyPair(pki.NodeCertFile, pki.NodeKeyFile)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		client.Cert = &pair
	}
	return client, files
}

// runEnroll is SPEC 7.3 from the node's side.
func runEnroll(args *cli.Args) int {
	n := setup(args)
	client, files := n.hubClient(args)
	ctx := context.Background()

	if client.Cert != nil && !args.Bool("force", false) {
		cert, err := files.ReadCert(pki.NodeCertFile)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		fmt.Printf("this node already holds a certificate for %s, expiring %s\n",
			n.nodeID, cert.NotAfter.UTC().Format(time.RFC3339))
		fmt.Println("`halite-node renew` replaces it; --force starts again from a new key")
		return 0
	}

	key, err := nodeKey(files, args, args.Bool("force", false))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	fingerprint, err := pki.FingerprintKey(key.Public())
	if err != nil {
		cli.Fatalf("%v", err)
	}
	fmt.Printf("node        %s\n", n.nodeID)
	fmt.Printf("fingerprint %s\n", fingerprint)
	fmt.Printf("hub         %s\n\n", client.HubURL)

	token := args.Flag("token", "")
	wait := args.Bool("wait", false)
	interval := 5 * time.Second

	for {
		got, err := client.Enroll(ctx, key, n.nodeID, token)
		switch {
		case errors.Is(err, transport.ErrPending):
			if !wait {
				fmt.Println("the request is waiting for an operator to accept it.")
				fmt.Printf("on the hub: halite-hub keys accept %s\n", n.nodeID)
				fmt.Println("run this again, or pass --wait, once it has been accepted.")
				// Exit 2 rather than 0: nothing is wrong, and nothing
				// has finished either, and a script that treats those
				// the same will start a node that cannot talk.
				return 2
			}
			fmt.Printf("waiting for an operator to accept %s...\n", fingerprint)
			select {
			case <-time.After(interval):
			case <-ctx.Done():
				return 1
			}
			continue
		case err != nil:
			cli.Fatalf("%v", err)
		}

		if err := writeIdentity(files, got); err != nil {
			cli.Fatalf("%v", err)
		}
		fmt.Printf("enrolled: %s\n", files.Path(pki.NodeCertFile))
		// SPEC 7.2: the identity is pinned at first enrollment, so a
		// DNS or DHCP change afterwards does not produce a second node.
		if n.cfg.Bool("node_id_caching", true) {
			if err := pinNodeID(args, n.nodeID); err != nil {
				n.log.Warn("could not pin the node identity", "error", err.Error())
			}
		}
		return 0
	}
}

// nodeKey loads the node's private key, or generates one. The key never
// leaves the node, which is the property the whole section rests on.
func nodeKey(files pki.Files, args *cli.Args, force bool) (crypto.Signer, error) {
	if files.Exists(pki.NodeKeyFile) && !force {
		return files.ReadKey(pki.NodeKeyFile)
	}
	alg, err := pki.ParseKeyAlgorithm(args.Flag("key-algorithm", string(pki.ECDSAP256)))
	if err != nil {
		return nil, err
	}
	key, err := pki.GenerateKey(alg)
	if err != nil {
		return nil, err
	}
	if force && files.Exists(pki.NodeKeyFile) {
		// WriteKey refuses to overwrite, deliberately. --force is the
		// operator saying to start again, so the old key is moved
		// aside rather than destroyed.
		aside := files.Path(pki.NodeKeyFile) + "." + time.Now().UTC().Format("20060102T150405")
		if err := os.Rename(files.Path(pki.NodeKeyFile), aside); err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "the previous key is at %s\n", aside)
	}
	if err := files.WriteKey(pki.NodeKeyFile, key); err != nil {
		return nil, err
	}
	return key, nil
}

func writeIdentity(files pki.Files, got *transport.Enrollment) error {
	if err := files.WriteCertPEM(pki.NodeCertFile, got.CertPEM); err != nil {
		return err
	}
	if len(got.CAPEM) > 0 {
		return files.WriteCertPEM(pki.CACertFile, got.CAPEM)
	}
	return nil
}

func pinNodeID(args *cli.Args, nodeID string) error {
	path := filepath.Join(args.Flag("root", config.DefaultRoot), "node_id")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(nodeID+"\n"), 0o644)
}

// runRenew is SPEC 7.4's renewal: no operator, no token, on the
// authenticated connection the node already has.
func runRenew(args *cli.Args) int {
	n := setup(args)
	client, files := n.hubClient(args)
	if client.Cert == nil {
		cli.Fatalf("this node holds no certificate to renew; `halite-node enroll` is the first step")
	}
	cert, err := files.ReadCert(pki.NodeCertFile)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if !args.Bool("force", false) && !needsRenewal(cert) {
		fmt.Printf("the certificate is good until %s; renewal is due at half its life\n",
			cert.NotAfter.UTC().Format(time.RFC3339))
		return 0
	}

	// A new key at every renewal, so that a stolen one has the bounded
	// life SPEC 7.4 promises rather than a bounded certificate over a
	// permanent key.
	alg, err := pki.ParseKeyAlgorithm(args.Flag("key-algorithm", string(pki.ECDSAP256)))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	key, err := pki.GenerateKey(alg)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	got, err := client.Renew(context.Background(), key, n.nodeID)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	// The key is written only once the hub has issued against it: a
	// node that replaced its key and then failed to get a certificate
	// would have locked itself out.
	aside := files.Path(pki.NodeKeyFile) + "." + time.Now().UTC().Format("20060102T150405")
	if err := os.Rename(files.Path(pki.NodeKeyFile), aside); err != nil {
		cli.Fatalf("%v", err)
	}
	if err := files.WriteKey(pki.NodeKeyFile, key); err != nil {
		cli.Fatalf("%v", err)
	}
	if err := writeIdentity(files, got); err != nil {
		cli.Fatalf("%v", err)
	}
	fresh, err := files.ReadCert(pki.NodeCertFile)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	fmt.Printf("renewed: %s expires %s\n", n.nodeID, fresh.NotAfter.UTC().Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "the previous key is at %s\n", aside)
	return 0
}

// needsRenewal is the halfway point of SPEC 7.4.
func needsRenewal(cert *x509.Certificate) bool {
	life := cert.NotAfter.Sub(cert.NotBefore)
	if life <= 0 {
		return true
	}
	return !time.Now().Before(cert.NotBefore.Add(life / 2))
}

// runConnect opens the subscribe stream and stays on it.
//
// This build carries the transport and not yet the work: a job arriving
// here is logged and refused rather than run, because running it needs
// the job cache and the return path that follow in this phase. It is
// the difference between "the fleet is connected" and "the fleet does
// what it is told", and saying which one this is matters.
func runConnect(args *cli.Args) int {
	n := setup(args)
	client, _ := n.hubClient(args)
	if client.Cert == nil {
		cli.Fatalf("this node is not enrolled; `halite-node enroll` is the first step")
	}

	// The hub's tree replaces the node's own, unless --local says
	// otherwise. A node with a hub should be applying the tree the hub
	// serves; SPEC section 13.
	n.attachToHub(args, client)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The executor runs jobs; the loop below reads the stream. They are
	// separate goroutines with a bounded queue between them, per SPEC
	// 9.6, so a state run that takes ten minutes does not stop the node
	// hearing a revocation.
	returns := make(chan *job.Return, 64)
	exec := newExecutor(n, int(n.cfg.Int("job_queue_depth", 16)), func(ret *job.Return) {
		select {
		case returns <- ret:
		default:
			n.log.Error("dropping a return because the return queue is full",
				"jid", string(ret.JID))
		}
	})
	n.executor = exec
	go exec.Run(ctx.Done())
	go n.postReturns(ctx, args, returns)
	go n.refreshGrains(ctx, args)
	n.startBeacons(ctx)
	n.startSchedule(ctx)
	n.startMine(ctx)

	backoff := time.Second
	const maxBackoff = time.Minute
	tries := n.cfg.Int("hub_tries", 0)
	attempt := int64(0)

	for ctx.Err() == nil {
		attempt++
		// Rebuilt on every reconnect so that a renewal that happened while
		// this process was running takes effect: the certificate is
		// read from disk here, not once at startup.
		client, _ = n.hubClient(args)
		// Attached again on every attempt, not once at startup. See
		// attachToHub.
		n.attachToHub(args, client)
		n.log.Info("connecting", "hub", client.HubURL)
		err := client.Subscribe(ctx, transport.SubscribeRequest{
			NodeID:  n.nodeID,
			Grains:  grainsJSON(n),
			Version: version.String(),
		}, func(msg transport.Message) error {
			return n.handle(msg)
		})
		if ctx.Err() != nil {
			break
		}
		if errors.Is(err, errRevoked) {
			// Reconnecting after a revocation is not a retry, it is a
			// node that will not take no for an answer. It stops.
			return 1
		}
		if err != nil {
			n.log.Warn("the stream ended", "error", err.Error())
		} else {
			// A clean end is the hub asking for a reconnection, so the
			// next attempt is immediate rather than backed off.
			n.log.Info("the hub closed the stream")
			backoff = time.Second
		}
		if tries > 0 && attempt >= tries {
			n.log.Error("giving up", "attempts", attempt)
			return 1
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
	n.log.Info("stopped")
	return 0
}

// refreshGrains re-collects this node's facts on the configured
// interval and pushes them.
//
// Without it the hub's idea of a node is as old as its last
// connection, and a node that has been up for a month is targeted on
// month-old facts. SPEC 8.3's `grain_stale_after` annotates that
// hazard; this is what stops it arising.
func (n *node) refreshGrains(ctx context.Context, args *cli.Args) {
	interval := n.cfg.Duration("grains_refresh_interval", 30*time.Minute)
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		root := args.Flag("root", config.DefaultRoot)
		fresh, warnings := grains.Collect(grains.Options{
			NodeID:     n.nodeID,
			StaticFile: root + "/grains",
			GrainsDir:  root + "/grains.d",
			Extra:      n.cfg.Map("grains"),
			Cloud:      n.cfg.Bool("cloud_grains", false),
		})
		for _, w := range warnings {
			n.log.Warn(w.String(), "component", "grains")
		}
		n.grains = fresh
		encoded, err := value.EncodeJSON(fresh, 0)
		if err != nil {
			n.log.Warn("could not encode refreshed grains", "error", err.Error())
			continue
		}
		client, _ := n.hubClient(args)
		if err := client.PushGrains(ctx, transport.GrainsRequest{
			Grains: encoded, Version: version.String(),
		}); err != nil && ctx.Err() == nil {
			n.log.Warn("could not push refreshed grains", "error", err.Error())
			continue
		}
		n.log.Debug("pushed refreshed grains", "keys", fresh.Len())
	}
}

// grainsJSON encodes the node's facts through the ordered model's own
// codec, so a 64-bit grain arrives at the hub as the integer it is.
func grainsJSON(n *node) json.RawMessage {
	raw, err := value.EncodeJSON(n.grains, 0)
	if err != nil {
		n.log.Warn("could not encode grains for the hub", "error", err.Error())
		return nil
	}
	return raw
}

// attachToHub points this node at the hub's tree, pillar, bus, and
// mine.
//
// Called on every connection attempt rather than once at startup, which
// is what it used to be. A node that starts before its hub is listening
// falls back to its own roots, and then stayed there for the life of
// the process -- managing itself from whatever local tree it happened
// to have, long after the hub came up and it reconnected successfully.
// In an estate where a hub and its nodes reboot together, that is the
// node most likely to win the race, and the only sign of it is one
// warning at startup.
//
// The bus and the mine are simply re-pointed at the current client,
// because the client is rebuilt on every reconnect and the old one
// holds a certificate that may since have been renewed. The tree and
// the pillar each cost a probe, so they are attempted only while they
// are not attached.
func (n *node) attachToHub(args *cli.Args, client *transport.Client) {
	if args.Bool("local", false) {
		return
	}
	n.useHubEvents(client)
	n.useHubMine(client)
	if !n.hubTree {
		n.useHubTree(client)
	}
	if n.hubPillar == nil {
		n.useHubPillar(client)
	}
}

// useHubTree points the node at the file server rather than at its own
// directories.
//
// The environments come from the hub's manifest at first use. A hub
// that cannot be asked leaves the node on its local tree with a warning
// rather than failing: a node that stops managing itself because the
// file server is briefly unreachable is worse than one that applies the
// tree it already has.
func (n *node) useHubTree(client *transport.Client) {
	cacheDir := n.cfg.String("cache_dir", config.DefaultCacheDir)
	remote := fileserver.NewRemote(client, cacheDir, nil)
	remote.HashType = n.cfg.String("hash_type", "sha256")

	m, err := remote.ManifestFor(n.env, "")
	if err != nil {
		n.log.Warn("the hub is not serving files; this node will apply its own tree",
			"error", err.Error())
		return
	}
	remote.Environments = []string{n.env}
	n.files = remote
	n.hubTree = true
	n.log.Info("compiling against the hub's tree",
		"env", n.env, "files", len(m.Files), "cache", cacheDir)
}

// useHubIfConfigured points a one-shot command at the hub, the way
// `salt-call` uses its master unless told `--local`. // lexicon:allow
//
// A node with no hub, one told `--local`, or one that has not enrolled
// works from its own roots exactly as before. A hub that cannot be
// reached is a warning and a local compilation rather than a failure:
// `pillar items` during an outage should still answer.
func (n *node) useHubIfConfigured(args *cli.Args) {
	if args.Bool("local", false) {
		return
	}
	if args.Flag("hub", n.cfg.String("hub", "")) == "" {
		return
	}
	files := pki.Files{Dir: args.Flag("pki-dir", n.cfg.String("pki_dir", config.DefaultPKIDir))}
	if !files.Exists(pki.NodeCertFile) || !files.Exists(pki.CACertFile) {
		return
	}
	client, _ := n.hubClient(args)
	n.useHubTree(client)
	n.useHubPillar(client)
	n.useHubEvents(client)
	n.useHubMine(client)
}

// useHubEvents lets a module on this node put a record on the hub's
// bus, which is what `event.send` is.
func (n *node) useHubEvents(client *transport.Client) {
	n.events = hubEvents{client: client, log: n.log}
}

// hubEvents forwards an event to the hub.
type hubEvents struct {
	client *transport.Client
	log    *hlog.Logger
}

func (h hubEvents) Send(tag string, data map[string]any) error {
	var encoded json.RawMessage
	if len(data) > 0 {
		raw, err := value.EncodeJSON(data, 0)
		if err != nil {
			return fmt.Errorf("the event payload will not encode: %w", err)
		}
		encoded = raw
	}
	res, err := h.client.SendEvent(context.Background(), transport.EventRequest{
		Tag:  tag,
		Data: encoded,
	})
	if err != nil {
		return err
	}
	h.log.Debug("event sent", "tag", res.Tag, "offset", res.Offset)
	return nil
}

// useHubPillar points pillar compilation at the hub.
//
// A hub that compiles no pillar leaves the node on its own roots: an
// estate migrating one piece at a time should not lose its pillar the
// moment it enrols.
func (n *node) useHubPillar(client *transport.Client) {
	grains, err := value.EncodeJSON(n.grains, 0)
	if err != nil {
		n.log.Warn("could not encode grains for the hub", "error", err.Error())
		return
	}
	probe, err := client.Pillar(context.Background(), transport.PillarRequest{
		NodeID: n.nodeID, Env: n.pillarEnv, Grains: grains,
	})
	if err != nil {
		if transport.CodeOf(err) == transport.CodeNoPillar {
			// The hub does not do pillar. Falling back to this node's
			// own roots is the point: an estate migrating one piece at
			// a time should not lose its pillar the moment it enrols.
			n.log.Warn("the hub compiles no pillar; this node will compile its own",
				"error", err.Error())
			return
		}
		// The hub does do pillar, and this node's did not compile.
		// Falling back here would substitute an empty local pillar for
		// the real one and let every state that reads pillar render
		// against nothing — a file written with no users in it, an
		// authorized_keys with no keys, reported as a successful
		// convergence. The error is carried instead, so `test.ping`
		// still answers and anything reading pillar fails saying why.
		n.log.Error("the hub could not compile this node's pillar; "+
			"pillar is unavailable and states that read it will fail",
			"error", err.Error())
		failure := err
		n.hubPillar = func(string) (*value.Map, error) { return nil, failure }
		return
	}
	n.log.Info("pillar comes from the hub", "env", probe.Env, "sls", len(probe.SLS))

	n.hubPillar = func(env string) (*value.Map, error) {
		// Asked again for every run rather than cached: pillar is
		// where an operator changes a value expecting the next
		// highstate to use it, and a cache would mean it does not.
		res, err := client.Pillar(context.Background(), transport.PillarRequest{
			NodeID: n.nodeID, Env: env, Grains: grains,
		})
		if err != nil {
			return nil, err
		}
		decoded, err := value.DecodeJSON(res.Pillar)
		if err != nil {
			return nil, fmt.Errorf("the pillar the hub sent is not readable: %w", err)
		}
		m, ok := decoded.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("the pillar the hub sent is not a mapping")
		}
		// Every value in it may be a secret, so the redactor learns
		// them all before anything can print one.
		n.secrets.AddTree(m)
		return m, nil
	}
}

// acceptJob turns a stream message into a job and queues it, or
// returns the refusal.
//
// SPEC 6.3: a node refuses a replayed, expired, or malformed job with a
// structured refusal rather than dropping it, because an operator
// watching a job that vanished learns nothing.
func (n *node) acceptJob(msg transport.Message) {
	j := &job.Job{
		JID:   job.ID(msg.JID),
		Fun:   msg.Fun,
		Arg:   msg.Arg,
		Kwarg: msg.Kwarg,
		Env:   msg.Env,
		Nonce: msg.Nonce,
	}
	if msg.Expires != "" {
		expires, err := time.Parse(time.RFC3339Nano, msg.Expires)
		if err != nil {
			n.refuse(j, fmt.Errorf("the job's expiry %q is not a timestamp: %w", msg.Expires, err))
			return
		}
		j.Expires = expires
	}
	if n.executor == nil {
		n.refuse(j, errors.New("this node is not running jobs"))
		return
	}
	if err := n.executor.Offer(j); err != nil {
		n.refuse(j, err)
		return
	}
}

// refuse files a refusal as a return.
func (n *node) refuse(j *job.Job, err error) {
	n.log.Warn("refusing a job", "jid", string(j.JID), "fun", j.Fun, "reason", err.Error())
	if n.executor == nil || j.JID == "" {
		return
	}
	select {
	case n.refusals <- refusalReturn(n, j, err):
	default:
	}
}

// postReturns sends finished returns to the hub, retrying a transient
// failure: a return that is lost because the connection blinked is a
// job that looks unresponsive for ever.
func (n *node) postReturns(ctx context.Context, args *cli.Args, returns <-chan *job.Return) {
	send := func(ret *job.Return) {
		client, _ := n.hubClient(args)
		for attempt := 0; attempt < 5; attempt++ {
			err := client.Return(ctx, ret)
			if err == nil {
				return
			}
			if ctx.Err() != nil {
				return
			}
			if transport.Permanent(err) {
				// The hub understood and said no. Retrying a refusal
				// is how a node spends its evening arguing with a hub
				// that has already answered.
				n.log.Warn("the hub refused a return",
					"jid", string(ret.JID), "error", err.Error())
				return
			}
			n.log.Warn("could not post a return", "jid", string(ret.JID), "error", err.Error())
			select {
			case <-time.After(time.Duration(attempt+1) * time.Second):
			case <-ctx.Done():
				return
			}
		}
		n.log.Error("giving up on a return", "jid", string(ret.JID))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ret := <-returns:
			send(ret)
		case ret := <-n.refusals:
			send(ret)
		}
	}
}

// errRevoked ends the connect loop rather than restarting it.
var errRevoked = errors.New("this node's enrollment has been revoked")

// handle is what a node does with one message from the hub.
func (n *node) handle(msg transport.Message) error {
	switch msg.T {
	case transport.MsgPing:
		n.log.Debug("ping", "seq", msg.Seq)
	case transport.MsgEvent:
		n.log.Info("event", "tag", msg.Tag)
	case transport.MsgRevoke:
		// SPEC 7.4: the node stops. Deleting the key material is the
		// specified behaviour and is not done here, because a node that
		// erases its identity on an unauthenticated instruction is a
		// denial of service waiting to happen -- the message arrives on
		// a mutually authenticated stream, but the operator's intent
		// and a hub compromise look identical from here.
		n.log.Error("this node's enrollment has been revoked", "reason", msg.Reason)
		return errRevoked
	case transport.MsgReload:
		n.log.Info("the hub asked this node to reconnect", "reason", msg.Reason)
	case transport.MsgKill:
		// A job the executor has not started yet is dropped. One
		// already running finishes: a state run interrupted halfway
		// leaves a machine in neither state, which is worse than
		// finishing something an operator changed their mind about.
		if n.executor != nil {
			dropped := n.executor.Cancel(job.ID(msg.JID))
			n.log.Warn("a job was cancelled by the hub",
				"jid", msg.JID, "reason", msg.Reason, "dropped", dropped)
		}
	case transport.MsgJob:
		n.acceptJob(msg)
	default:
		n.log.Info("message", "type", msg.T)
	}
	return nil
}
