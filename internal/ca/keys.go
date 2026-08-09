// Package ca is halite's tiny certificate authority. It replaces Salt's
// minion key accept/reject dance with a standard CSR flow: an agent
// generates a key and a certificate signing request, the master stores the
// request as pending, an operator accepts it, and the agent collects a
// client certificate. Everything is stdlib crypto/x509 and inspectable with
// openssl.
package ca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// keyFileMode is the permission every private key is written with. A key
// readable by anyone else is a compromised key.
const keyFileMode os.FileMode = 0o600

// idPattern constrains agent identities to safe, filename-shaped strings.
// Identities arrive over the network and are used to build paths, so this
// is the trust boundary: no separators, no traversal, no surprises.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateID rejects identities that are unsafe to use as a filename.
func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid id %q: use 1-64 chars of letters, digits, dot, dash, underscore", id)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("invalid id %q", id)
	}
	return nil
}

// GenerateKey creates a P-256 key and its PEM encoding. P-256 keeps the
// certificates small and works with every TLS 1.2+ stack and openssl build.
func GenerateKey() (crypto.Signer, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// NewCSR builds a certificate signing request for the given identity.
func NewCSR(key crypto.Signer, commonName string, dnsNames []string) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: commonName},
		DNSNames: dnsNames,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, fmt.Errorf("create csr: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// ParseCSR decodes a PEM CSR and verifies its self-signature, which proves
// the requester holds the private key for the embedded public key.
func ParseCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("not a PEM certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("csr signature: %w", err)
	}
	return csr, nil
}

// Fingerprint is the SHA-256 of a DER blob, formatted like ssh-keygen's
// hex form, for out-of-band comparison before accepting a key.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
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

// CSRFingerprint fingerprints the public key inside a PEM CSR.
func CSRFingerprint(csrPEM []byte) (string, error) {
	csr, err := ParseCSR(csrPEM)
	if err != nil {
		return "", err
	}
	return Fingerprint(csr.RawSubjectPublicKeyInfo), nil
}

func parseCertPEM(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseKeyPEM(keyPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("not a PEM private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("key of type %T cannot sign", key)
	}
	return signer, nil
}

// writeNew writes a file only if it does not already exist, so issuing a
// certificate never silently destroys an existing key.
func writeNew(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}
