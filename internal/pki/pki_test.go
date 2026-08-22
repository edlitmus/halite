package pki

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"
)

func testCA(t *testing.T) *CA {
	t.Helper()
	ca, err := NewCA(ECDSAP256, "halite test CA", 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func TestIssueAndVerifyANodeCertificate(t *testing.T) {
	ca := testCA(t)
	key, err := GenerateKey(ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := NewNodeCSR(key, "web1.example")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := DecodeCSR(EncodeCSR(csrDER))
	if err != nil {
		t.Fatal(err)
	}

	der, err := ca.IssueNode(csr, "web1.example", 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := DecodeCert(EncodeCert(der))
	if err != nil {
		t.Fatal(err)
	}

	// The identity lives in the URI SAN, and only there.
	id, err := NodeIDFromCert(cert)
	if err != nil {
		t.Fatal(err)
	}
	if id != "web1.example" {
		t.Errorf("identity = %q", id)
	}

	// It chains to the CA and is good for client authentication.
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("the certificate should chain to its CA: %v", err)
	}
}

// A request cannot ask for an identity by putting one in the CSR. That
// mistake turns an enrollment system into an identity free-for-all: the
// operator accepts "web1" and the node has asked to be "hub".
func TestTheRequestCannotChooseItsOwnIdentity(t *testing.T) {
	ca := testCA(t)
	key, _ := GenerateKey(ECDSAP256)
	csrDER, _ := NewNodeCSR(key, "attacker-chosen")
	csr, err := DecodeCSR(EncodeCSR(csrDER))
	if err != nil {
		t.Fatal(err)
	}

	der, err := ca.IssueNode(csr, "what-the-hub-decided", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := DecodeCert(EncodeCert(der))
	id, err := NodeIDFromCert(cert)
	if err != nil {
		t.Fatal(err)
	}
	if id != "what-the-hub-decided" {
		t.Errorf("the issued identity is %q; the CSR asked for another and got it", id)
	}
	if cert.Subject.CommonName != "what-the-hub-decided" {
		t.Errorf("the common name is %q", cert.Subject.CommonName)
	}
	if len(cert.URIs) != 1 {
		t.Errorf("the certificate carries %d URIs; one identity means one", len(cert.URIs))
	}
}

// A certificate with no URI SAN has no identity here, whatever its
// common name says. Falling back to the common name would mean a
// certificate could carry two identities and the checker picks one.
func TestACommonNameIsNotAnIdentity(t *testing.T) {
	cert := &x509.Certificate{}
	cert.Subject.CommonName = "web1.example"
	if _, err := NodeIDFromCert(cert); err == nil {
		t.Error("a common name alone should not be read as an identity")
	} else if !strings.Contains(err.Error(), "halite://node/") {
		t.Errorf("the error should say where the identity lives: %v", err)
	}
}

// A request whose signature does not verify proves nothing about who
// holds the key, and the proof is the whole point of the request.
func TestAnUnsignedRequestIsRefused(t *testing.T) {
	key, _ := GenerateKey(ECDSAP256)
	csrDER, _ := NewNodeCSR(key, "web1")
	pemBytes := EncodeCSR(csrDER)

	// Corrupt the signature by flipping a byte near the end, which is
	// where it lives.
	broken := append([]byte{}, csrDER...)
	broken[len(broken)-1] ^= 0xff
	if _, err := DecodeCSR(EncodeCSR(broken)); err == nil {
		t.Error("a request with a broken signature should be refused")
	}
	if _, err := DecodeCSR(pemBytes); err != nil {
		t.Errorf("the intact request should still verify: %v", err)
	}
}

// The fingerprint is of the public key, not of the request: the request
// is rebuilt on every retry and its bytes change while the key stays the
// same, and a fingerprint that changes when nothing changed is one
// nobody checks.
func TestFingerprintFollowsTheKeyNotTheRequest(t *testing.T) {
	key, _ := GenerateKey(ECDSAP256)
	first, _ := NewNodeCSR(key, "web1")
	second, _ := NewNodeCSR(key, "web1")

	a, err := fingerprintOf(t, first)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := fingerprintOf(t, second)
	if a != b {
		t.Errorf("two requests for one key fingerprinted differently:\n  %s\n  %s", a, b)
	}
	if strings.Count(a, ":") != 31 {
		t.Errorf("a fingerprint should be 32 colon-separated pairs, for reading aloud: %s", a)
	}

	other, _ := GenerateKey(ECDSAP256)
	otherCSR, _ := NewNodeCSR(other, "web1")
	c, _ := fingerprintOf(t, otherCSR)
	if c == a {
		t.Error("two different keys fingerprinted the same")
	}
}

func fingerprintOf(t *testing.T, csrDER []byte) (string, error) {
	t.Helper()
	csr, err := DecodeCSR(EncodeCSR(csrDER))
	if err != nil {
		return "", err
	}
	return FingerprintCSR(csr)
}

func TestKeyAlgorithmNames(t *testing.T) {
	for name, want := range map[string]KeyAlgorithm{
		"": ECDSAP256, "ecdsa": ECDSAP256, "p256": ECDSAP256, "ecdsa-p256": ECDSAP256,
		"p384": ECDSAP384, "ECDSA-P384": ECDSAP384,
	} {
		got, err := ParseKeyAlgorithm(name)
		if err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", name, got, err, want)
		}
	}
	// Ed25519 is deliberately not the default and is not offered here
	// yet; asking for it should say so rather than quietly give ECDSA.
	if _, err := ParseKeyAlgorithm("ed25519"); err == nil {
		t.Error("an algorithm this build does not issue should be refused by name")
	}
}
