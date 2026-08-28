package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// The CA is public, so an attacker can put the real one in the chain
// beside their own leaf. Finding the pinned fingerprint in the chain is
// therefore not enough: without verifying the leaf against it, the node
// pins the right CA and is still talking to the wrong hub.
func TestAForeignLeafBesideTheRealCAIsRefused(t *testing.T) {
	real, err := pki.NewCA(pki.ECDSAP256, "halite enrollment CA", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rogue, err := pki.NewCA(pki.ECDSAP256, "halite enrollment CA", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	// A leaf the rogue CA signed, presented with the *real* CA appended.
	der, err := rogue.IssueHub(key, []string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
			tc := tls.Server(c, &tls.Config{
				MinVersion: tls.VersionTLS13,
				NextProtos: []string{ALPN, Negotiated},
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{der, real.Cert.Raw},
					PrivateKey:  key,
				}},
			})
			_ = tc.Handshake()
			_ = tc.Close()
		}
	}()

	want, err := pki.FingerprintCert(real.Cert)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FetchCA(context.Background(), "https://"+ln.Addr().String(), want, 5*time.Second)
	if err == nil {
		t.Fatal("a leaf signed by another CA was accepted because the real CA was in the chain")
	}
	if got != nil {
		t.Error("a CA was returned from a refused handshake")
	}
	if !strings.Contains(err.Error(), "chain") {
		t.Logf("refused with: %v", err)
	}

	// And the honest case still works: a leaf the real CA signed.
	honest, err := real.IssueHub(key, []string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	go func() {
		for {
			c, err := ln2.Accept()
			if err != nil {
				return
			}
			tc := tls.Server(c, &tls.Config{
				MinVersion: tls.VersionTLS13,
				NextProtos: []string{ALPN, Negotiated},
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{honest, real.Cert.Raw},
					PrivateKey:  key,
				}},
			})
			_ = tc.Handshake()
			_ = tc.Close()
		}
	}()
	ca, err := FetchCA(context.Background(), "https://"+ln2.Addr().String(), want, 5*time.Second)
	if err != nil {
		t.Fatalf("the honest hub was refused: %v", err)
	}
	if ca == nil || !ca.Equal(real.Cert) {
		t.Error("the CA fetched is not the one the hub serves")
	}
	var _ *x509.Certificate = ca
}

// Without a fingerprint there is nothing to check the CA against, and
// this function would be a way to trust whatever answered. It is the
// guard that keeps the bootstrap from being bare trust-on-first-use, so
// it is refused before a connection is even attempted.
func TestFetchingACAWithoutAFingerprintIsRefused(t *testing.T) {
	// An address nothing listens on: reaching the dial at all would
	// mean the guard did not fire.
	_, err := FetchCA(context.Background(), "https://127.0.0.1:1", "", time.Second)
	if err == nil {
		t.Fatal("a CA was fetched with no fingerprint to check it against")
	}
	if !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// A hub presenting a chain with no certificate matching the pin is the
// ordinary man-in-the-middle case.
func TestAChainWithoutThePinnedCAIsRefused(t *testing.T) {
	rogue, err := pki.NewCA(pki.ECDSAP256, "halite enrollment CA", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	real, err := pki.NewCA(pki.ECDSAP256, "halite enrollment CA", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	der, err := rogue.IssueHub(key, []string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
			tc := tls.Server(c, &tls.Config{
				MinVersion: tls.VersionTLS13,
				NextProtos: []string{ALPN, Negotiated},
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{der, rogue.Cert.Raw},
					PrivateKey:  key,
				}},
			})
			_ = tc.Handshake()
			_ = tc.Close()
		}
	}()

	want, err := pki.FingerprintCert(real.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FetchCA(context.Background(), "https://"+ln.Addr().String(), want, 5*time.Second); err == nil {
		t.Fatal("a chain with an entirely different CA was accepted")
	}
}
