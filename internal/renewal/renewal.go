// Package renewal is SPEC 7.4's renewal of an identity enrolled with a
// hub: a new key, a certificate for it from the hub, both written, the
// old key kept aside, and the loop that decides when.
//
// Two programs hold such an identity. A node renews its own from
// `halite-node connect`; a relay holds one for its upstream, in
// `relay_pki_dir`, and runs `halite-hub serve`, which renewed nothing, so
// every relay stopped authenticating upstream 90 days after it enrolled
// (DIVERGENCE 5.263). The sequence lives here rather than in either
// command so that the two cannot drift apart: the order of the writes,
// which key is kept, and when renewal is due are the same question for
// both, and a copy would have been two answers.
package renewal

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// Renewed is what a successful renewal produced.
type Renewed struct {
	// Cert is the new certificate, read back from disk.
	Cert *x509.Certificate
	// Aside is where the previous key was moved.
	Aside string
	// Pruned are the keys earlier renewals set aside, now removed, and
	// PruneErr why one could not be. A failed prune is not a failed
	// renewal: the new identity is written and works.
	Pruned   []string
	PruneErr error
}

// Identity renews the identity in files over client, which must be
// authenticated by the certificate being renewed. alg "" keeps the
// algorithm of the key held now.
//
// The key is written only once the hub has issued against it: one that
// replaced its key and then failed to get a certificate would have locked
// itself out. The caller holds whatever stops a reconnect from reading
// the files half written; the hub revokes the old serial when it issues
// the new one, so a reconnect between the two writes presents a serial
// that has just been denied.
func Identity(ctx context.Context, client *transport.Client, files pki.Files, nodeID string, alg pki.KeyAlgorithm) (Renewed, error) {
	if alg == "" {
		current, err := files.ReadKey(pki.NodeKeyFile)
		if err != nil {
			return Renewed{}, err
		}
		if alg, err = pki.AlgorithmOf(current); err != nil {
			return Renewed{}, err
		}
	}
	// A new key at every renewal, so that a stolen one has the bounded
	// life SPEC 7.4 promises rather than a bounded certificate over a
	// permanent key.
	key, err := pki.GenerateKey(alg)
	if err != nil {
		return Renewed{}, err
	}
	got, err := client.Renew(ctx, key, nodeID)
	if err != nil {
		return Renewed{}, err
	}
	aside, err := SetKeyAside(files, time.Now())
	if err != nil {
		return Renewed{}, err
	}
	out := Renewed{Aside: aside}
	if err := files.WriteKey(pki.NodeKeyFile, key); err != nil {
		return out, err
	}
	if err := WriteIdentity(files, got); err != nil {
		return out, err
	}
	if out.Cert, err = files.ReadCert(pki.NodeCertFile); err != nil {
		return out, err
	}
	// Only now, with the new key and its certificate both written and
	// read back: until then an earlier key is still the way back.
	out.Pruned, out.PruneErr = PruneKeysAside(files, aside)
	return out, nil
}

// WriteIdentity writes the certificate an enrollment or a renewal
// returned, and the CA with it when the hub sent one.
func WriteIdentity(files pki.Files, got *transport.Enrollment) error {
	if err := files.WriteCertPEM(pki.NodeCertFile, got.CertPEM); err != nil {
		return err
	}
	if len(got.CAPEM) > 0 {
		return files.WriteCertPEM(pki.CACertFile, got.CAPEM)
	}
	return nil
}

// KeyAsidePrefix names a key a renewal moved aside, as opposed to one
// `enroll --force` did. They used to share `node.key.<time>`, and
// nothing pruned either, so a node collected a private key for every
// renewal -- one every 45 days on the default lifetime -- each for a
// certificate the hub had already revoked (DIVERGENCE 5.222). Only the
// renewal's are pruned: a key an operator moved aside by re-enrolling
// was a decision, and may be the one copy of an identity they meant to
// keep.
const KeyAsidePrefix = pki.NodeKeyFile + ".renewed."

// SetKeyAside moves the current key to node.key.renewed.<UTC time> and
// returns where.
func SetKeyAside(files pki.Files, now time.Time) (string, error) {
	aside := files.Path(KeyAsidePrefix + now.UTC().Format("20060102T150405"))
	if err := atomicfile.Rename(files.Path(pki.NodeKeyFile), aside); err != nil {
		return "", err
	}
	return aside, nil
}

// PruneKeysAside removes every key an earlier renewal set aside, keeping
// keep -- the one this renewal just made, which is the way back if the
// new identity turns out to be bad. A key named the old way, before
// renewals were told apart from re-enrollments, is left alone: it cannot
// be told which it was.
func PruneKeysAside(files pki.Files, keep string) ([]string, error) {
	entries, err := os.ReadDir(files.Dir)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, KeyAsidePrefix) {
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

// Loop checks the certificate current reads, at once and then on
// pki.RenewalCheckEvery, and calls renew once it is due (pki.DueForRenewal).
// A failure is passed to warn and tried again at the next check, never
// fatal: the certificate it failed to replace still works until it
// expires. whose names the certificate in those messages -- "this node's",
// "this relay's upstream" -- because a hub that is a relay logs about two
// certificates and the operator needs to know which one is failing.
// It returns when ctx ends.
func Loop(ctx context.Context, whose string,
	current func() (*x509.Certificate, error),
	renew func() (*x509.Certificate, error),
	warn func(msg string, kv ...any)) {
	for {
		every := time.Hour
		cert, err := current()
		switch {
		case err != nil:
			warn(fmt.Sprintf("could not read %s certificate to see whether it is due for renewal", whose),
				"component", "pki", "error", err.Error())
		case pki.DueForRenewal(cert, time.Now()):
			every = pki.RenewalCheckEvery(cert)
			if fresh, err := renew(); err != nil {
				warn(fmt.Sprintf("renewing %s certificate failed; trying again at the next check", whose),
					"component", "pki", "expires", cert.NotAfter.UTC().Format(time.RFC3339),
					"next_check", every.String(), "error", err.Error())
			} else {
				every = pki.RenewalCheckEvery(fresh)
			}
		default:
			every = pki.RenewalCheckEvery(cert)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
