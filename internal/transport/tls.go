// Package transport is the substrate of SPEC section 6: HTTP/2 over TLS
// 1.3 with mutual X.509 authentication, on one TCP port.
//
// Everything here is crypto/tls and net/http. Section 6.1 records why at
// length, and the short version is that framing, multiplexing, flow
// control, authentication, confidentiality, integrity, replay
// resistance, and peer identity are each present in the standard library
// and were each hand-rolled by the thing this replaces.
//
// There is no plaintext mode. Not for loopback, not for a test, not
// behind a flag — section 6.1 is explicit that such a setting is
// invariably found in production, and the way to be sure of that is for
// it not to exist.
package transport

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"slices"
	"sync"

	"github.com/edlitmus/halite/internal/pki"
)

// ALPN is the protocol identifier of SPEC 6.1. A peer that does not
// offer it is rejected at handshake, which keeps a stray HTTPS client
// from reaching any endpoint at all.
const ALPN = "halite/1"

// Negotiated is the protocol actually selected, and it is "h2" rather
// than ALPN.
//
// net/http runs its bundled HTTP/2 server only for the exact identifier
// "h2": a connection that negotiates anything else is closed without a
// byte of HTTP, and there is no exported way to register another name
// for it. Offering "halite/1" and selecting it would therefore mean
// either giving up HTTP/2 or vendoring an HTTP/2 implementation, and
// SPEC section 6.1 wants HTTP/2 more than it wants the string.
//
// So halite/1 stays mandatory in the offer -- requireProtocol below
// refuses a ClientHello without it, at the handshake, which is the
// effect section 6.1 asks the identifier for -- and h2 is what gets
// selected. Recorded in DIVERGENCE.md.
const Negotiated = "h2"

// DefaultPort is SPEC 6.1's one TCP port.
const DefaultPort = 4510

// Denylist holds revoked certificate serials.
//
// SPEC 7.4: revocation is immediate and does not depend on a CRL
// propagating, so the check is in the handshake rather than in a list
// the peer is trusted to have fetched.
type Denylist struct {
	mu      sync.RWMutex
	serials map[string]string
}

func NewDenylist() *Denylist { return &Denylist{serials: map[string]string{}} }

// Revoke adds a serial, with the reason an operator gave.
func (d *Denylist) Revoke(serial, reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.serials[serial] = reason
}

// Allow removes a serial, for a revocation entered in error.
func (d *Denylist) Allow(serial string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.serials, serial)
}

// Revoked reports whether a serial is denied, and why.
func (d *Denylist) Revoked(serial string) (string, bool) {
	if d == nil {
		return "", false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	reason, ok := d.serials[serial]
	return reason, ok
}

// Serials lists what is revoked, for `keys list` and the CRL.
func (d *Denylist) Serials() map[string]string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[string]string, len(d.serials))
	for k, v := range d.serials {
		out[k] = v
	}
	return out
}

// ServerConfig builds the hub's TLS configuration.
//
// Client certificates are requested and verified. `/v1/health` is the
// one endpoint reachable without one, which is why the requirement is
// VerifyClientCertIfGiven rather than RequireAndVerify: the handler
// decides, and everything except health refuses an anonymous peer.
func ServerConfig(cert tls.Certificate, ca *x509.Certificate, denied *Denylist) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	cfg := &tls.Config{
		MinVersion:            tls.VersionTLS13,
		MaxVersion:            tls.VersionTLS13,
		NextProtos:            []string{Negotiated},
		Certificates:          []tls.Certificate{cert},
		ClientAuth:            tls.VerifyClientCertIfGiven,
		ClientCAs:             pool,
		VerifyPeerCertificate: verifyNotRevoked(denied),
	}
	cfg.GetConfigForClient = requireProtocol
	return cfg
}

// requireProtocol refuses a ClientHello that does not offer halite/1.
//
// Returning a nil configuration keeps the one the listener was built
// with; returning an error ends the handshake before a certificate is
// exchanged, so a stray HTTPS client never reaches an endpoint.
func requireProtocol(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	if slices.Contains(hello.SupportedProtos, ALPN) {
		return nil, nil
	}
	return nil, fmt.Errorf("peer %s does not speak %s (it offered %v)",
		hello.Conn.RemoteAddr(), ALPN, hello.SupportedProtos)
}

// ClientConfig builds a node's TLS configuration.
func ClientConfig(cert tls.Certificate, ca *x509.Certificate, serverName string) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
		NextProtos:   []string{ALPN, Negotiated},
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
	}
}

// EnrollConfig is the configuration a node uses before it has a
// certificate: it verifies the hub and presents nothing.
//
// This is the one anonymous client, and it exists so that enrollment can
// happen at all. It still verifies the hub against the CA the operator
// pinned, so an unenrolled node cannot be talked to by anything else.
func EnrollConfig(ca *x509.Certificate, serverName string) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		NextProtos: []string{ALPN, Negotiated},
		RootCAs:    pool,
		ServerName: serverName,
	}
}

// verifyNotRevoked rejects a peer whose serial is on the denylist, at
// the handshake, before any handler sees the connection.
func verifyNotRevoked(denied *Denylist) func([][]byte, [][]*x509.Certificate) error {
	return func(_ [][]byte, chains [][]*x509.Certificate) error {
		if denied == nil {
			return nil
		}
		for _, chain := range chains {
			if len(chain) == 0 {
				continue
			}
			serial := pki.SerialString(chain[0])
			if reason, revoked := denied.Revoked(serial); revoked {
				id, _ := pki.NodeIDFromCert(chain[0])
				return fmt.Errorf("the certificate for %s (serial %s) is revoked: %s", id, serial, reason)
			}
		}
		return nil
	}
}
