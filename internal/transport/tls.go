package transport

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/edlitmus/halite/internal/ca"
)

// minTLS is the floor for every connection. There is no downgrade path and
// no configuration knob: this is a closed fleet, not the public web.
const minTLS = tls.VersionTLS13

// loadPool reads a PEM bundle into a certificate pool.
func loadPool(caCertPath string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read ca certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s contains no usable certificate", caCertPath)
	}
	return pool, nil
}

// ServerTLS builds the control plane's TLS configuration. Client
// certificates are requested but not required at the handshake, because
// enrollment has to work before an agent has one; every handler other than
// enrollment then insists on a verified certificate.
func ServerTLS(certPath, keyPath, caCertPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load server certificate: %w", err)
	}
	pool, err := loadPool(caCertPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   minTLS,
		NextProtos:   []string{"h2"},
	}, nil
}

// ClientTLS builds a client configuration that trusts only the fleet CA.
// certPath may be empty during enrollment, when the client has a key and a
// pending request but no certificate yet.
func ClientTLS(certPath, keyPath, caCertPath string) (*tls.Config, error) {
	pool, err := loadPool(caCertPath)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		RootCAs:    pool,
		MinVersion: minTLS,
		NextProtos: []string{"h2"},
	}
	if certPath == "" {
		return cfg, nil
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load client certificate: %w", err)
	}
	cfg.Certificates = []tls.Certificate{cert}
	return cfg, nil
}

// NewClient returns an HTTP/2 client for the given TLS configuration.
// Timeout bounds ordinary requests; long polls override it per request.
func NewClient(cfg *tls.Config, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		TLSClientConfig:     cfg,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// Peer is the authenticated identity behind a request.
type Peer struct {
	ID   string
	Role ca.Role
}

// PeerOf extracts the verified client identity from a request. A request
// with no verified chain has no identity — the certificate is the only
// thing that establishes who the caller is.
func PeerOf(r *http.Request) (Peer, bool) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		return Peer{}, false
	}
	cert := r.TLS.VerifiedChains[0][0]
	role := ca.RoleAgent
	for _, ou := range cert.Subject.OrganizationalUnit {
		switch ou {
		case ca.OUAdmins:
			role = ca.RoleAdmin
		case ca.OUServers:
			role = ca.RoleServer
		}
	}
	if cert.Subject.CommonName == "" {
		return Peer{}, false
	}
	return Peer{ID: cert.Subject.CommonName, Role: role}, true
}
