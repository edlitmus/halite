package hub

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// A certificate stored while the listener is open is the one the next
// handshake gets: the hub renews its own certificate without a restart.
// DIVERGENCE 5.259.
func TestTheNextHandshakeGetsARenewedCertificate(t *testing.T) {
	ca, err := pki.NewCA(pki.ECDSAP256, "test CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	issue := func() tls.Certificate {
		key, err := pki.GenerateKey(pki.ECDSAP256)
		if err != nil {
			t.Fatal(err)
		}
		der, err := ca.IssueHub(key, []string{"127.0.0.1"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	first, second := issue(), issue()
	serving := NewServingCert(first)
	ln, err := Listen("127.0.0.1:0", serving, ca.Cert, transport.NewDenylist())
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

	presented := func() []byte {
		conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
			InsecureSkipVerify: true, NextProtos: []string{transport.ALPN, transport.Negotiated},
			MinVersion: tls.VersionTLS13,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].Raw
	}
	if got := presented(); string(got) != string(first.Certificate[0]) {
		t.Fatal("the listener did not present the certificate it was opened with")
	}
	serving.Store(second)
	if got := presented(); string(got) != string(second.Certificate[0]) {
		leaf, _ := x509.ParseCertificate(got)
		t.Fatalf("after Store, the next handshake still got serial %v", leaf.SerialNumber)
	}
}
