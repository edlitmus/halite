package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/pki"
)

// lockIdentity takes identityMu and returns its release. A node built
// without one -- only a test's, which is never concurrent -- has nothing
// to lock.
func (n *node) lockIdentity() func() {
	if n.identityMu == nil {
		return func() {}
	}
	n.identityMu.Lock()
	return n.identityMu.Unlock
}

// renewIdentity is SPEC 7.4's renewal, shared by `halite-node renew` and
// the connect loop: a new key, a certificate for it from the hub, both
// written, the old key kept aside. It returns rather than exits, because
// the connect loop's caller is a goroutine whose failure must be a log
// line and a retry, not the end of the agent.
//
// alg "" keeps the algorithm of the key the node holds. The flag on
// `halite-node renew` used to default to P-256, so a node enrolled on
// P-384 became a P-256 one the first time anybody renewed it.
func (n *node) renewIdentity(args *cli.Args, alg pki.KeyAlgorithm) (*x509.Certificate, string, error) {
	client, files := n.hubClient(args)
	if client.Cert == nil {
		return nil, "", fmt.Errorf("this node holds no certificate to renew; `halite-node enroll` is the first step")
	}

	// Held across the hub's answer as well as the writes. The hub
	// revokes the old serial when it issues the new one and asks the
	// node's stream to reconnect; a reconnect that read the files before
	// they were rewritten would present the serial just denied.
	defer n.lockIdentity()()

	if alg == "" {
		current, err := files.ReadKey(pki.NodeKeyFile)
		if err != nil {
			return nil, "", err
		}
		if alg, err = pki.AlgorithmOf(current); err != nil {
			return nil, "", err
		}
	}
	// A new key at every renewal, so that a stolen one has the bounded
	// life SPEC 7.4 promises rather than a bounded certificate over a
	// permanent key.
	key, err := pki.GenerateKey(alg)
	if err != nil {
		return nil, "", err
	}
	got, err := client.Renew(context.Background(), key, n.nodeID)
	if err != nil {
		return nil, "", err
	}
	// The key is written only once the hub has issued against it: a
	// node that replaced its key and then failed to get a certificate
	// would have locked itself out.
	aside, err := setRenewedKeyAside(files, time.Now())
	if err != nil {
		return nil, "", err
	}
	if err := files.WriteKey(pki.NodeKeyFile, key); err != nil {
		return nil, aside, err
	}
	if err := writeIdentity(files, got); err != nil {
		return nil, aside, err
	}
	fresh, err := files.ReadCert(pki.NodeCertFile)
	if err != nil {
		return nil, aside, err
	}
	// Only now, with the new key and its certificate both written and
	// read back: until then an earlier key is still the way back.
	if removed, err := pruneRenewedKeys(files, aside); err != nil {
		n.log.Warn("could not remove a key an earlier renewal set aside",
			"error", err.Error(), "removed", len(removed))
	} else if len(removed) > 0 {
		n.log.Info("removed keys earlier renewals set aside", "count", len(removed))
	}
	return fresh, aside, nil
}

// renewedKeyPrefix names a key a renewal moved aside, as opposed to one
// `enroll --force` did. They used to share `node.key.<time>`, and
// nothing pruned either, so a node collected a private key for every
// renewal -- one every 45 days on the default lifetime -- each for a
// certificate the hub had already revoked (DIVERGENCE 5.222). Only the
// renewal's are pruned: a key an operator moved aside by re-enrolling
// was a decision, and may be the one copy of an identity they meant to
// keep.
const renewedKeyPrefix = pki.NodeKeyFile + ".renewed."

// setRenewedKeyAside moves the current key to node.key.renewed.<UTC time>
// and returns where.
func setRenewedKeyAside(files pki.Files, now time.Time) (string, error) {
	aside := files.Path(renewedKeyPrefix + now.UTC().Format("20060102T150405"))
	if err := atomicfile.Rename(files.Path(pki.NodeKeyFile), aside); err != nil {
		return "", err
	}
	return aside, nil
}

// pruneRenewedKeys removes every key an earlier renewal set aside, keeping
// keep -- the one this renewal just made, which is the way back if the
// new identity turns out to be bad. A key named the old way, before
// renewals were told apart from re-enrollments, is left alone: it cannot
// be told which it was.
func pruneRenewedKeys(files pki.Files, keep string) ([]string, error) {
	entries, err := os.ReadDir(files.Dir)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, renewedKeyPrefix) {
			continue
		}
		path := files.Path(name)
		if path == keep {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, err
		}
		removed = append(removed, path)
	}
	return removed, nil
}

// renewalCheckEvery is how often the connect loop looks at the
// certificate: a twentieth of its life, between ten seconds and an hour.
// An hour is ample for the 90-day default, whose renewal falls due on
// day 45; scaling down with the lifetime is what lets a hub configured
// with a short `certificate_lifetime` -- a test, or an estate that wants
// one -- still get renewals in time, without a setting of its own.
func renewalCheckEvery(cert *x509.Certificate) time.Duration {
	every := cert.NotAfter.Sub(cert.NotBefore) / 20
	switch {
	case every < 10*time.Second:
		return 10 * time.Second
	case every > time.Hour:
		return time.Hour
	}
	return every
}

// keepRenewed is the renewal SPEC 7.4 says needs no operator. It used to
// need one: the specification, the operations guide and the setting's
// own documentation all said renewal was automatic, and the only caller
// of the renewal was `halite-node renew`, which nothing ran -- so every
// node would have stopped authenticating 90 days after it enrolled
// (DIVERGENCE 5.195).
//
// It runs for as long as `connect` does. It checks at once, then on
// renewalCheckEvery. A failure is logged and tried again at the next
// check, never fatal: the certificate it failed to replace still works
// until it expires, and `doctor` warns a fortnight before that.
func (n *node) keepRenewed(ctx context.Context, args *cli.Args) {
	n.runRenewals(ctx, func() (*x509.Certificate, error) {
		_, files := n.hubClient(args)
		release := n.lockIdentity()
		defer release()
		return files.ReadCert(pki.NodeCertFile)
	}, func() (*x509.Certificate, error) {
		fresh, aside, err := n.renewIdentity(args, "")
		if err == nil {
			n.log.Info("renewed this node's certificate", "component", "pki",
				"expires", fresh.NotAfter.UTC().Format(time.RFC3339), "previous_key", aside)
		}
		return fresh, err
	})
}

// runRenewals is keepRenewed's loop with the two things it touches
// passed in, so a test can drive the schedule without a hub.
func (n *node) runRenewals(ctx context.Context,
	current func() (*x509.Certificate, error),
	renew func() (*x509.Certificate, error)) {
	for {
		every := time.Hour
		cert, err := current()
		switch {
		case err != nil:
			n.log.Warn("could not read this node's certificate to see whether it is due for renewal",
				"component", "pki", "error", err.Error())
		case needsRenewal(cert):
			every = renewalCheckEvery(cert)
			if fresh, err := renew(); err != nil {
				n.log.Warn("renewing this node's certificate failed; trying again at the next check",
					"component", "pki", "expires", cert.NotAfter.UTC().Format(time.RFC3339),
					"next_check", every.String(), "error", err.Error())
			} else {
				every = renewalCheckEvery(fresh)
			}
		default:
			every = renewalCheckEvery(cert)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
