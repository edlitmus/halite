package ca

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Store is a PKI directory:
//
//	ca.crt, ca.key            the authority itself
//	master.crt, master.key    the control plane's server certificate
//	pending/<id>.csr          requests awaiting an operator decision
//	accepted/<id>.crt         issued agent certificates
//	rejected/<id>.csr         refused requests, kept for the audit trail
type Store struct {
	Dir string
}

func (s *Store) path(parts ...string) string {
	return filepath.Join(append([]string{s.Dir}, parts...)...)
}

// Role decides the key usages and organizational unit of an issued
// certificate. Agents may only authenticate as clients, the control plane
// may only serve, and operators are agents' peers on the wire but are the
// only role allowed to dispatch work.
type Role int

const (
	RoleAgent Role = iota
	RoleServer
	RoleAdmin
)

// Organizational units stamped into issued certificates. The control plane
// authorizes requests by role, so the role has to travel inside the
// certificate rather than being claimed by the caller.
const (
	OUAgents  = "agents"
	OUServers = "servers"
	OUAdmins  = "admins"
)

func (r Role) ou() string {
	switch r {
	case RoleServer:
		return OUServers
	case RoleAdmin:
		return OUAdmins
	default:
		return OUAgents
	}
}

// Init creates the CA key and self-signed certificate. It is an error to
// initialize a store that already has one — rotating a CA is a deliberate
// act, not an accident.
func (s *Store) Init(commonName string, validFor time.Duration) error {
	if _, err := os.Stat(s.path("ca.crt")); err == nil {
		return fmt.Errorf("%s already exists: refusing to overwrite the CA", s.path("ca.crt"))
	}
	key, keyPEM, err := GenerateKey()
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return fmt.Errorf("create ca certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeNew(s.path("ca.key"), keyPEM, keyFileMode); err != nil {
		return fmt.Errorf("write ca key: %w", err)
	}
	if err := writeNew(s.path("ca.crt"), certPEM, 0o644); err != nil {
		return fmt.Errorf("write ca certificate: %w", err)
	}
	for _, dir := range []string{"pending", "accepted", "rejected"} {
		if err := os.MkdirAll(s.path(dir), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// CACertPEM returns the CA certificate, which agents need as their trust
// root and which is safe to distribute.
func (s *Store) CACertPEM() ([]byte, error) {
	return os.ReadFile(s.path("ca.crt"))
}

func (s *Store) loadCA() (*x509.Certificate, crypto.Signer, error) {
	certPEM, err := os.ReadFile(s.path("ca.crt"))
	if err != nil {
		return nil, nil, fmt.Errorf("read ca certificate (run 'halite key init'): %w", err)
	}
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(s.path("ca.key"))
	if err != nil {
		return nil, nil, fmt.Errorf("read ca key: %w", err)
	}
	key, err := parseKeyPEM(keyPEM)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// Sign issues a certificate for a verified CSR. The subject common name is
// taken from the CA's caller, never from the request, so a requester cannot
// choose its own identity; SANs are likewise supplied by the caller.
func (s *Store) Sign(csrPEM []byte, commonName string, role Role, hosts []string, validFor time.Duration) ([]byte, error) {
	csr, err := ParseCSR(csrPEM)
	if err != nil {
		return nil, err
	}
	caCert, caKey, err := s.loadCA()
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         commonName,
			OrganizationalUnit: []string{role.ou()},
		},
		NotBefore: now.Add(-time.Hour),
		NotAfter:  now.Add(validFor),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	switch role {
	case RoleServer:
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		for _, h := range hosts {
			if ip := net.ParseIP(h); ip != nil {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
				continue
			}
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	default:
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		tmpl.DNSNames = []string{commonName}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, csr.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("sign certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// IssueLocal generates a key pair on the CA host and signs it in one step,
// writing <dir>/<prefix>.key and <dir>/<prefix>.crt. It is for credentials
// the CA operator holds anyway — the control plane's own certificate and
// operator certificates. Agents never use this path: their keys are
// generated on the agent and only the request travels.
func (s *Store) IssueLocal(dir, prefix, commonName string, role Role, hosts []string, validFor time.Duration) error {
	key, keyPEM, err := GenerateKey()
	if err != nil {
		return err
	}
	csrPEM, err := NewCSR(key, commonName, nil)
	if err != nil {
		return err
	}
	certPEM, err := s.Sign(csrPEM, commonName, role, hosts, validFor)
	if err != nil {
		return err
	}
	if err := writeNew(filepath.Join(dir, prefix+".key"), keyPEM, keyFileMode); err != nil {
		return fmt.Errorf("write %s key: %w", prefix, err)
	}
	if err := writeNew(filepath.Join(dir, prefix+".crt"), certPEM, 0o644); err != nil {
		return fmt.Errorf("write %s certificate: %w", prefix, err)
	}
	return nil
}

// IssueServer writes the control plane's master.key and master.crt.
func (s *Store) IssueServer(commonName string, hosts []string, validFor time.Duration) error {
	return s.IssueLocal(s.Dir, "master", commonName, RoleServer, hosts, validFor)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("serial number: %w", err)
	}
	return serial, nil
}
