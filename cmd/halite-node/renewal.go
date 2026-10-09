package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/renewal"
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

	got, err := renewal.Identity(context.Background(), client, files, n.nodeID, alg)
	if err != nil {
		return nil, got.Aside, err
	}
	if got.PruneErr != nil {
		n.log.Warn("could not remove a key an earlier renewal set aside",
			"error", got.PruneErr.Error(), "removed", len(got.Pruned))
	} else if len(got.Pruned) > 0 {
		n.log.Info("removed keys earlier renewals set aside", "count", len(got.Pruned))
	}
	return got.Cert, got.Aside, nil
}

// renewalCheckEvery is how often the connect loop looks at the
// certificate; see pki.RenewalCheckEvery, which the hub uses too.
func renewalCheckEvery(cert *x509.Certificate) time.Duration {
	return pki.RenewalCheckEvery(cert)
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
// passed in, so a test can drive the schedule without a hub. The loop is
// renewal.Loop, which a relay's upstream identity runs too.
func (n *node) runRenewals(ctx context.Context,
	current func() (*x509.Certificate, error),
	renew func() (*x509.Certificate, error)) {
	renewal.Loop(ctx, "this node's", current, renew, n.log.Warn)
}
