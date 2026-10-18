package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/fips"
)

// **In FIPS mode the API offers only the approved curves** (issue 321,
// section 5). The hub's listener restricts key exchange to P-256 and
// P-384 itself (SPEC 25.3, 27.4); the API's left it to Go. With go1.27.2
// that was enough -- under fips140=on Go refuses X25519 on a listener with
// no curve list -- so this passes against the old listener too, and holds
// the property rather than the change: the API now sets the curves itself,
// as the hub does. Runs only in a FIPS build, which is how `make
// fips-test` and CI's FIPS job run this package.
func TestTheAPIListenerRefusesAnUnapprovedCurveInFIPSMode(t *testing.T) {
	if !fips.Restricted() {
		t.Skip("not a FIPS build; run under `make fips-test` or GODEBUG=fips140=on")
	}
	cert := testCert(t)
	ln, err := Listen("127.0.0.1:0", func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	dial := func(curve tls.CurveID) error {
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
			InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{curve},
		})
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := dial(tls.X25519); err == nil {
		t.Error("a client offering only X25519 completed a handshake in FIPS mode")
	}
	if err := dial(tls.CurveP256); err != nil {
		t.Errorf("a client offering P-256 was refused: %v", err)
	}
}

func testCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "api"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
