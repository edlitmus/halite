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
	"sync"
	"testing"
	"time"
)

// selfSignedTLS builds a certificate for 127.0.0.1, and the pool that
// verifies it, written to a file the client can be pointed at.
var (
	certOnce sync.Once
	certPEM  []byte
	tlsPair  tls.Certificate
)

func selfSignedTLS(t *testing.T) *tls.Config {
	t.Helper()
	certOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		template := x509.Certificate{
			SerialNumber:          big.NewInt(1),
			Subject:               pkix.Name{CommonName: "127.0.0.1"},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().Add(24 * time.Hour),
			IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
			KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			IsCA:                  true,
			BasicConstraintsValid: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
		})
		tlsPair, err = tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
	})
	return &tls.Config{Certificates: []tls.Certificate{tlsPair}, MinVersion: tls.VersionTLS12}
}

// caFile writes the fake's certificate where a client can read it.
func caFile(t *testing.T) string {
	t.Helper()
	selfSignedTLS(t)
	path := t.TempDir() + "/ca.pem"
	if err := writeFile(path, certPEM); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

// newFakeDirectory starts a directory for this package's own tests.
func newFakeDirectory(t *testing.T) *fakeDirectory {
	t.Helper()
	d, err := newDirectory(selfSignedTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.close)
	return d
}
