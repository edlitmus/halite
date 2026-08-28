package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// FetchCA obtains the hub's enrollment CA from the hub itself, trusting
// it only if it matches a fingerprint the operator delivered by another
// route.
//
// This is the one place in the build that sets InsecureSkipVerify, and
// it is safe for one reason: the fingerprint is the trust anchor, not
// the connection. An attacker in the middle would have to present a
// certificate whose SHA-256 matches the pinned one, which is a preimage.
// SPEC 7.3 already assumes the CA travels a route that can be tampered
// with — that is why the fingerprint exists.
//
// Two things make it safe that are easy to leave out:
//
//   - The check runs inside the handshake, so a chain that does not
//     satisfy it fails the connection rather than being noticed
//     afterwards by a caller who might forget.
//   - Finding the pinned CA in the chain is not enough. The CA is
//     public, so an attacker can put the real one in a chain beside
//     their own leaf; the leaf is therefore verified against a pool
//     holding only the matched CA. Without that the node would pin the
//     right CA and still be talking to the wrong hub.
func FetchCA(ctx context.Context, hubURL, fingerprint string, timeout time.Duration) (*x509.Certificate, error) {
	if fingerprint == "" {
		// Never a bare trust-on-first-use. Without a fingerprint there
		// is nothing to check against, and this function would be a
		// way to trust whatever answered.
		return nil, fmt.Errorf("fetching the hub CA needs a pinned fingerprint to check it against")
	}
	parsed, err := url.Parse(hubURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", hubURL, err)
	}
	host := parsed.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, fmt.Sprintf("%d", DefaultPort))
	}

	var found *x509.Certificate
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		NextProtos: []string{ALPN, Negotiated},
		// Replaced wholesale by the callback below, which does more
		// than the default verifier rather than less.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			ca, leaf, err := matchPinned(rawCerts, fingerprint)
			if err != nil {
				return err
			}
			pool := x509.NewCertPool()
			pool.AddCert(ca)
			// The name is not checked here: a node enrolling by IP
			// address is ordinary, and the fingerprint is what
			// identifies the hub. The certificate is verified for
			// signature, validity, and chain against the pinned CA
			// alone.
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
				return fmt.Errorf("the hub's certificate does not chain to the CA "+
					"whose fingerprint this node pinned: %w", err)
			}
			found = ca
			return nil
		},
	}

	dialer := &tls.Dialer{Config: cfg}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := dialer.DialContext(dialCtx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("fetching the hub CA from %s: %w", host, err)
	}
	_ = conn.Close()
	if found == nil {
		// Unreachable while the callback is the only path to a
		// successful handshake, and asserted rather than assumed: this
		// returning nil with a nil error would be a node trusting
		// nothing and believing it had checked something.
		return nil, fmt.Errorf("the handshake with %s completed without producing a CA", host)
	}
	return found, nil
}

// matchPinned finds the certificate in a presented chain whose
// fingerprint the operator pinned, and answers with it and the leaf.
func matchPinned(rawCerts [][]byte, fingerprint string) (ca, leaf *x509.Certificate, err error) {
	if len(rawCerts) == 0 {
		return nil, nil, fmt.Errorf("the hub presented no certificate")
	}
	leaf, err = x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return nil, nil, fmt.Errorf("the hub's certificate could not be read: %w", err)
	}
	for _, raw := range rawCerts {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			continue
		}
		got, err := pki.FingerprintCert(cert)
		if err != nil {
			continue
		}
		if pki.FingerprintEqual(fingerprint, got) {
			return cert, leaf, nil
		}
	}
	return nil, nil, fmt.Errorf(
		"no certificate the hub presented matches the fingerprint this node pinned (%s); "+
			"check `hub_fingerprint` against `halite-hub keys fingerprint` on the hub", fingerprint)
}
