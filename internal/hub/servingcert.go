package hub

import (
	"crypto/tls"
	"sync/atomic"
)

// ServingCert is the certificate the hub presents, which its renewal
// replaces while the listener stays open.
//
// The hub loaded hub.crt once and handed the pair to the listener, so a
// renewal had nothing to replace and there was no renewal: the hub
// served the certificate it started with until the day it expired, and
// from then every node's handshake failed until somebody restarted it.
// DIVERGENCE 5.259.
//
// Not internal/certreload, which re-reads files on every handshake for a
// certificate something else renews. The hub renews this one itself and
// knows when it has, and what it serves is not the file: it carries the
// enrollment CA after the leaf, which a node enrolling for the first
// time checks its pinned fingerprint against (withCA in cmd/halite-hub).
type ServingCert struct {
	p atomic.Pointer[tls.Certificate]
}

// NewServingCert serves pair until Store replaces it.
func NewServingCert(pair tls.Certificate) *ServingCert {
	s := &ServingCert{}
	s.p.Store(&pair)
	return s
}

// Store replaces the certificate. A handshake already under way keeps
// the one it started with; the next one gets this.
func (s *ServingCert) Store(pair tls.Certificate) { s.p.Store(&pair) }

// Load is the certificate being served.
func (s *ServingCert) Load() *tls.Certificate { return s.p.Load() }

// GetCertificate is tls.Config.GetCertificate.
func (s *ServingCert) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return s.Load(), nil
}
