package pki

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"strings"
)

// A fingerprint is what an operator compares out of band before
// accepting an enrollment, so its spelling matters more than most:
// it has to survive being read down a telephone, pasted into a ticket,
// and compared by eye against another copy.
//
// SPEC 7.3 has the operator compare "the CSR public key digest". The
// digest is of the public key rather than of the request, because the
// request is rebuilt on every retry and its bytes change while the key
// stays the same — a fingerprint that changes when nothing changed is a
// fingerprint nobody checks.

// Fingerprint is the SHA-256 digest of a DER public key, rendered as
// colon-separated pairs.
func Fingerprint(publicKeyDER []byte) string {
	sum := sha256.Sum256(publicKeyDER)
	hexed := hex.EncodeToString(sum[:])
	var b strings.Builder
	for i := 0; i < len(hexed); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hexed[i : i+2])
	}
	return b.String()
}

// FingerprintKey is the same digest taken from a public key directly,
// for a node that wants to print what it is about to ask for.
func FingerprintKey(pub any) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

// FingerprintCSR is the fingerprint an operator compares before
// accepting a request.
func FingerprintCSR(csr *x509.CertificateRequest) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

// FingerprintCert is the same digest taken from an issued certificate,
// so that what an operator accepted and what a node presents can be
// compared without keeping the request.
func FingerprintCert(cert *x509.Certificate) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

// FingerprintEqual compares two fingerprints written by different
// hands.
//
// `halite-hub keys fingerprint` prints colon-separated pairs, which is
// what reads aloud; a configuration file, a ticket, or a script may
// carry the same digest as bare hex or with a `sha256:` prefix, because
// that is how every other tool spells it. Refusing one of those
// spellings would mean a node that will not enrol and a fingerprint
// that looks right.
func FingerprintEqual(a, b string) bool {
	return normalizeFingerprint(a) == normalizeFingerprint(b)
}

func normalizeFingerprint(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "sha256:")
	s = strings.TrimPrefix(s, "sha-256:")
	return strings.NewReplacer(":", "", " ", "", "-", "").Replace(s)
}

// SerialString renders a certificate serial the way the CRL and the
// revocation denylist spell it.
func SerialString(cert *x509.Certificate) string {
	return fmt.Sprintf("%x", cert.SerialNumber)
}
