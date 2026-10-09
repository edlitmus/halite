package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/certreload"
)

func writeServingPair(t *testing.T, certPath, keyPath string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 96))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "api.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return serial.String()
}

// The API's port presents a certificate renewed on disk from the next
// connection, without a restart, and still speaks TLS 1.3 and nothing
// less. It used to be handed one loaded pair and serve it for the life of
// the process. DIVERGENCE 5.250.
func TestTheAPIServesARenewedCertificateWithoutARestart(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "api.crt"), filepath.Join(dir, "api.key")
	first := writeServingPair(t, certPath, keyPath)
	certs, err := certreload.New(certPath, keyPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen("127.0.0.1:0", certs.GetCertificate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	addr := ln.Addr().String()
	served := func() string {
		t.Helper()
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatalf("handshake: %v", err)
		}
		defer conn.Close()
		if v := conn.ConnectionState().Version; v != tls.VersionTLS13 {
			t.Errorf("negotiated %#x, not TLS 1.3", v)
		}
		return conn.ConnectionState().PeerCertificates[0].SerialNumber.String()
	}

	if got := served(); got != first {
		t.Fatalf("served %s, the file holds %s", got, first)
	}
	second := writeServingPair(t, certPath, keyPath)
	if got := served(); got != second {
		t.Errorf("after a renewal on disk the API served %s, not the new %s", got, second)
	}

	if conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}); err == nil {
		conn.Close()
		t.Error("the API accepted TLS 1.2")
	}
}
