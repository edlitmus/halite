package certreload

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
)

// writePair writes a fresh self-signed certificate and its key to the two
// paths and answers with the certificate's serial.
func writePair(t *testing.T, certPath, keyPath string) string {
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
		Subject:      pkix.Name{CommonName: "localhost"},
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

// serve listens on loopback with the reloader and answers with the
// address. Each accepted connection completes its handshake and closes.
func serve(t *testing.T, r *Reloader) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{GetCertificate: r.GetCertificate, MinVersion: tls.VersionTLS13})
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
	return ln.Addr().String()
}

// served is the serial a fresh handshake is presented with. Verification
// is off on purpose: the question is which certificate, not whether it is
// trusted.
func served(t *testing.T, addr string) string {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.String()
}

// A pair replaced in place, and one replaced by rename, is served from
// the next connection. DIVERGENCE 5.248, 5.250.
func TestAReplacedPairIsServedFromTheNextConnection(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	first := writePair(t, certPath, keyPath)
	var said int
	r, err := New(certPath, keyPath, func(string, ...any) { said++ }, nil)
	if err != nil {
		t.Fatal(err)
	}
	addr := serve(t, r)
	if got := served(t, addr); got != first {
		t.Fatalf("served %s, the file holds %s", got, first)
	}

	second := writePair(t, certPath, keyPath)
	if got := served(t, addr); got != second {
		t.Errorf("after an in-place replacement: served %s, want %s", got, second)
	}

	staged := t.TempDir()
	sc, sk := filepath.Join(staged, "tls.crt"), filepath.Join(staged, "tls.key")
	third := writePair(t, sc, sk)
	if err := os.Rename(sk, keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sc, certPath); err != nil {
		t.Fatal(err)
	}
	if got := served(t, addr); got != third {
		t.Errorf("after a replacement by rename: served %s, want %s", got, third)
	}
	if said != 2 {
		t.Errorf("two reloads were said %d times", said)
	}
}

// A pair that will not load leaves the previous one in service, is said
// once however often it is met, and a good pair afterwards is picked up.
func TestAnUnloadablePairKeepsThePreviousOne(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	first := writePair(t, certPath, keyPath)
	var warned int
	r, err := New(certPath, keyPath, nil, func(string, ...any) { warned++ })
	if err != nil {
		t.Fatal(err)
	}
	addr := serve(t, r)

	staged := t.TempDir()
	sc, sk := filepath.Join(staged, "tls.crt"), filepath.Join(staged, "tls.key")
	next := writePair(t, sc, sk)
	if err := os.Rename(sk, keyPath); err != nil { // the key alone: a mismatched pair
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if got := served(t, addr); got != first {
			t.Fatalf("with a mismatched pair on disk the listener served %s, not the previous %s", got, first)
		}
	}
	if warned != 1 {
		t.Errorf("a mismatched pair met three times was said %d times", warned)
	}
	if err := os.Rename(sc, certPath); err != nil {
		t.Fatal(err)
	}
	if got := served(t, addr); got != next {
		t.Errorf("once the certificate arrived: served %s, want %s", got, next)
	}
}

// A pair that cannot be used at startup is an error then, as it was
// before there was a reloader.
func TestAnUnusablePairAtStartupIsAnError(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, err := New(certPath, keyPath, nil, nil); err == nil {
		t.Error("missing files were accepted at startup")
	}
}
