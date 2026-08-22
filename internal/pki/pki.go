// Package pki is the enrollment CA of SPEC section 7.
//
// A node's identity is an X.509 certificate issued by the hub, with the
// node ID in the subject common name and in a URI SAN of the form
// `halite://node/<node_id>`. The URI SAN is authoritative and the common
// name is for people to read: a common name is a free-text field that
// several things have historically been willing to match on, and a URI
// SAN has one meaning.
//
// The node's private key is generated on the node and never leaves it.
// The hub holds nothing that can impersonate a node, which is the one
// property that distinguishes this from Salt's arrangement, in which
// the master holds the minion's public key, // lexicon:allow
// and the trust is symmetric in practice if not in theory.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

// URIScheme is the authoritative identity SAN's scheme and host.
const URIScheme = "halite"

// NodeURI is the URI SAN that names a node.
func NodeURI(nodeID string) *url.URL {
	return &url.URL{Scheme: URIScheme, Host: "node", Path: "/" + nodeID}
}

// NodeIDFromCert reads the authoritative identity out of a certificate.
//
// Only the URI SAN is consulted. Falling back to the common name would
// mean a certificate could carry two identities and the checker would
// pick one, which is the shape of a great many certificate bugs.
func NodeIDFromCert(cert *x509.Certificate) (string, error) {
	for _, u := range cert.URIs {
		if u.Scheme != URIScheme || u.Host != "node" {
			continue
		}
		id := strings.TrimPrefix(u.Path, "/")
		if id == "" {
			return "", fmt.Errorf("the certificate's %s URI names no node", URIScheme)
		}
		return id, nil
	}
	return "", fmt.Errorf("the certificate carries no %s://node/<id> URI, which is where the identity lives", URIScheme)
}

// KeyAlgorithm selects the key to generate. SPEC 7.1 makes ECDSA P-256
// the default rather than Ed25519, which is the better algorithm and is
// not approved under FIPS 140-3.
type KeyAlgorithm string

const (
	ECDSAP256 KeyAlgorithm = "ecdsa-p256"
	ECDSAP384 KeyAlgorithm = "ecdsa-p384"
)

// ParseKeyAlgorithm reads the configured algorithm.
func ParseKeyAlgorithm(s string) (KeyAlgorithm, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ECDSAP256), "p256", "ecdsa":
		return ECDSAP256, nil
	case string(ECDSAP384), "p384":
		return ECDSAP384, nil
	}
	return "", fmt.Errorf("key_algorithm %q is not one this build issues; try %s or %s",
		s, ECDSAP256, ECDSAP384)
}

// GenerateKey makes a private key of the named algorithm.
func GenerateKey(alg KeyAlgorithm) (crypto.Signer, error) {
	switch alg {
	case ECDSAP384:
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	default:
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
}

// serialNumber draws a 128-bit random serial.
//
// A counter would be smaller and is what a great many small CAs use; a
// random serial is what stops a certificate being predictable before it
// is issued, and 128 bits is the floor the CA/Browser Forum settled on
// after the SHA-1 collision work.
func serialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

// EncodeKey renders a private key as PEM.
func EncodeKey(key crypto.Signer) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// DecodeKey reads a PEM private key.
func DecodeKey(data []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found where a private key was expected")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("the PEM block holds a %T, which cannot sign", key)
	}
	return signer, nil
}

// EncodeCert renders a certificate as PEM.
func EncodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// DecodeCert reads a PEM certificate.
func DecodeCert(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found where a certificate was expected")
	}
	return x509.ParseCertificate(block.Bytes)
}

// EncodeCSR renders a certificate request as PEM.
func EncodeCSR(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// DecodeCSR reads a PEM certificate request and checks its signature.
//
// The signature check is not optional and is not deferred: a CSR whose
// signature does not verify proves nothing about who holds the key, and
// the whole point of the request is that proof.
func DecodeCSR(data []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found where a certificate request was expected")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("the request's signature does not verify, so it proves nothing "+
			"about who holds the key: %w", err)
	}
	return csr, nil
}

// NewNodeCSR builds a certificate request for a node identity.
func NewNodeCSR(key crypto.Signer, nodeID string) ([]byte, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("a certificate request needs a node identity")
	}
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: nodeID},
		URIs:    []*url.URL{NodeURI(nodeID)},
	}
	return x509.CreateCertificateRequest(rand.Reader, tmpl, key)
}

// CA is an enrollment authority.
type CA struct {
	Cert *x509.Certificate
	Key  crypto.Signer
	// Now is the clock, so that a test can issue a certificate that has
	// already expired without waiting ninety days for it.
	Now func() time.Time
}

func (ca *CA) now() time.Time {
	if ca.Now != nil {
		return ca.Now()
	}
	return time.Now()
}

// NewCA creates a self-signed enrollment authority.
func NewCA(alg KeyAlgorithm, commonName string, lifetime time.Duration) (*CA, error) {
	key, err := GenerateKey(alg)
	if err != nil {
		return nil, err
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(lifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		// One level: the CA issues node and hub certificates and nothing
		// issues anything else. A path length nobody needs is a path
		// length nobody audits.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key}, nil
}

// IssueNode signs a node's certificate request.
//
// The request's subject and SANs are not copied. Only the node ID the
// hub decided on is used, so a request cannot ask for an identity by
// putting it in the CSR — which is the mistake that makes a great many
// enrollment systems into an identity free-for-all.
func (ca *CA) IssueNode(csr *x509.CertificateRequest, nodeID string, lifetime time.Duration) ([]byte, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("issuing a certificate needs a node identity")
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	now := ca.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID},
		URIs:         []*url.URL{NodeURI(nodeID)},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(lifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, csr.PublicKey, ca.Key)
}

// IssueHub signs the hub's own serving certificate.
func (ca *CA) IssueHub(key crypto.Signer, names []string, lifetime time.Duration) ([]byte, error) {
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	now := ca.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "halite hub"},
		DNSNames:     names,
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(lifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	return x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, key.Public(), ca.Key)
}
