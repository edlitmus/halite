package ldap

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// TestDirectory is an LDAP server for another package's tests.
//
// It holds a service account, an operator `ed` with the password
// `hunter2`, and the groups `platform` and `oncall` — enough for a
// caller to prove its login path reaches a directory and comes back
// with roles.
type TestDirectory struct {
	fake   *fakeDirectory
	caPath string
}

// NewTestDirectory starts one. The caller closes it.
func NewTestDirectory(dir string) (*TestDirectory, error) {
	cfg, certPEM, err := selfSignedFor("127.0.0.1")
	if err != nil {
		return nil, err
	}
	fake, err := newDirectory(cfg)
	if err != nil {
		return nil, err
	}
	caPath := filepath.Join(dir, "ldap-ca.pem")
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		fake.close()
		return nil, err
	}
	fake.user("cn=halite,ou=services,dc=example,dc=com", "service-pw")
	fake.user("uid=ed,ou=people,dc=example,dc=com", "hunter2")
	fake.entry(Entry{
		DN: "uid=ed,ou=people,dc=example,dc=com",
		Attributes: map[string][]string{
			"uid":  {"ed"},
			"mail": {"ed@example.com"},
			"memberOf": {
				"cn=platform,ou=groups,dc=example,dc=com",
				"cn=oncall,ou=groups,dc=example,dc=com",
			},
		},
	})
	return &TestDirectory{fake: fake, caPath: caPath}, nil
}

// Address is host:port.
func (d *TestDirectory) Address() string { return d.fake.address() }

// CAFile holds the certificate this directory serves with.
func (d *TestDirectory) CAFile() string { return d.caPath }

// Close stops it.
func (d *TestDirectory) Close() { d.fake.close() }

// selfSignedFor makes a certificate for one address, and returns it in
// PEM so a client can be pointed at it as a CA.
func selfSignedFor(ip string) (*tls.Config, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: ip},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IPAddresses:           []net.IP{net.ParseIP(ip)},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, certPEM, nil
}
