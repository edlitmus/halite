package builtin

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/fips"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// The x509 module of SPEC section 15.2: certificate and key generation,
// CSR creation, signing, and inspection, on crypto/x509 directly.
//
// Salt's x509 module needs M2Crypto or `cryptography`, both of which
// compile against OpenSSL headers, and getting either onto a node is the
// single most common reason a Salt install fails. There is nothing here
// to install: the standard library has the whole of it.
//
// Two rules run through this file. A private key is written 0600 and
// never returned by a function that a template could print. And every
// parameter that has a weak setting has a floor rather than a default —
// an RSA key smaller than 2048 bits is refused, not quietly upgraded,
// because a tree asking for 1024 should learn that it did.

// minRSABits is the smallest RSA key this module will generate.
//
// 2048 is the floor NIST SP 800-57 sets for use past 2030 and what every
// public CA requires. A tree asking for less is refused rather than
// silently given more: the tree is wrong, and it should be told.
const minRSABits = 2048

// defaultCertDays is how long a certificate lasts when a tree does not
// say. A year is short enough to force the renewal path to work and long
// enough not to surprise anyone.
const defaultCertDays = 365

// keySpec is a request for a private key.
type keySpec struct {
	Algorithm string // rsa, ec, ed25519
	Bits      int    // rsa
	Curve     string // ec: p256, p384, p521
}

func (k keySpec) describe() string {
	switch k.Algorithm {
	case "rsa":
		return fmt.Sprintf("rsa %d", k.Bits)
	case "ec":
		return "ec " + k.Curve
	}
	return k.Algorithm
}

// parseKeySpec reads the algorithm arguments a tree may write, and
// refuses the combinations that are wrong rather than guessing at them.
func parseKeySpec(algorithm string, bits int64, curve string) (keySpec, error) {
	k := keySpec{Algorithm: strings.ToLower(strings.TrimSpace(algorithm))}
	if k.Algorithm == "" {
		k.Algorithm = "rsa"
	}
	switch k.Algorithm {
	case "rsa":
		k.Bits = int(bits)
		if k.Bits == 0 {
			k.Bits = 4096
		}
		if k.Bits < minRSABits {
			return k, fmt.Errorf("an RSA key of %d bits is too small; %d is the minimum this module will generate",
				k.Bits, minRSABits)
		}
		if k.Bits%8 != 0 {
			return k, fmt.Errorf("an RSA key size must be a multiple of 8, found %d", k.Bits)
		}
	case "ec", "ecdsa":
		k.Algorithm = "ec"
		k.Curve = strings.ToLower(strings.TrimSpace(curve))
		if k.Curve == "" {
			k.Curve = "p256"
		}
		if _, err := curveByName(k.Curve); err != nil {
			return k, err
		}
	case "ed25519":
		// SPEC 27.4: Ed25519 is not approved under FIPS 140-3. Refused
		// by name rather than left to fail somewhere inside the module,
		// so the operator is told which setting to change.
		if fips.Restricted() {
			return k, fmt.Errorf("ed25519 is not approved under FIPS 140-3 and this process " +
				"is in FIPS mode; use algorithm: ec with curve p256 or p384 (SPEC 27.4)")
		}
	default:
		return k, fmt.Errorf("unknown key algorithm %q; halite generates rsa, ec, and ed25519 keys", algorithm)
	}
	return k, nil
}

func curveByName(name string) (elliptic.Curve, error) {
	switch strings.ToLower(strings.TrimPrefix(name, "secp")) {
	case "p256", "256r1", "prime256v1":
		return elliptic.P256(), nil
	case "p384", "384r1":
		return elliptic.P384(), nil
	case "p521", "521r1":
		return elliptic.P521(), nil
	}
	// P-224 is deliberately absent: it is below the 128-bit security
	// level and no longer accepted by public CAs.
	return nil, fmt.Errorf("unknown curve %q; halite generates p256, p384, and p521", name)
}

// generateKey makes a private key to a spec.
func generateKey(k keySpec) (crypto.Signer, error) {
	switch k.Algorithm {
	case "rsa":
		return rsa.GenerateKey(rand.Reader, k.Bits)
	case "ec":
		c, err := curveByName(k.Curve)
		if err != nil {
			return nil, err
		}
		return ecdsa.GenerateKey(c, rand.Reader)
	case "ed25519":
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		return priv, err
	}
	return nil, fmt.Errorf("unknown key algorithm %q", k.Algorithm)
}

// encodePrivateKey renders a key as PKCS#8 PEM, which every modern tool
// reads and which carries the algorithm inside it.
func encodePrivateKey(key crypto.Signer) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// loadPrivateKey reads a key from a path or from PEM text.
//
// Both PKCS#8 and the older PKCS#1 and SEC 1 forms are accepted, because
// an existing tree's keys were written by whatever tool made them.
func loadPrivateKey(source string) (crypto.Signer, error) {
	data, err := pemSource(source)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in the private key")
	}
	if strings.Contains(block.Type, "ENCRYPTED") {
		return nil, fmt.Errorf("the private key is encrypted; halite does not prompt for a passphrase")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		signer, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("the private key is not a signing key")
		}
		return signer, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("the private key is not in a form halite reads: PKCS#8, PKCS#1, and SEC 1 are supported")
}

// pemSource accepts either a path or the PEM text itself, which is how
// Salt trees pass these around.
func pemSource(source string) ([]byte, error) {
	if strings.Contains(source, "-----BEGIN") {
		return []byte(source), nil
	}
	if source == "" {
		return nil, fmt.Errorf("no path or PEM text was given")
	}
	return os.ReadFile(source)
}

// loadCertificate reads a certificate from a path or from PEM text.
func loadCertificate(source string) (*x509.Certificate, error) {
	data, err := pemSource(source)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in the certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// ---- subject and extension handling ----

// buildSubject reads the distinguished-name arguments a tree writes.
func buildSubject(args *value.Map) pkix.Name {
	get := func(names ...string) []string {
		for _, n := range names {
			if v, ok := args.Get(n); ok && v != nil {
				if s := value.KeyString(v); s != "" {
					return []string{s}
				}
			}
		}
		return nil
	}
	n := pkix.Name{
		Country:            get("C", "country"),
		Organization:       get("O", "organization"),
		OrganizationalUnit: get("OU", "organizational_unit"),
		Locality:           get("L", "locality"),
		Province:           get("ST", "state", "province"),
	}
	if cn := get("CN", "common_name"); len(cn) > 0 {
		n.CommonName = cn[0]
	}
	return n
}

// parseSANs splits the subject alternative names into the four kinds
// crypto/x509 keeps separately. A bare name is a DNS name, which is what
// a tree means nine times in ten.
func parseSANs(entries []string) (dns []string, ips []net.IP, emails []string, uris []*url.URL, err error) {
	for _, raw := range entries {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		kind, rest, hasPrefix := strings.Cut(e, ":")
		if hasPrefix {
			switch strings.ToUpper(strings.TrimSpace(kind)) {
			case "DNS":
				dns = append(dns, strings.TrimSpace(rest))
				continue
			case "IP":
				ip := net.ParseIP(strings.TrimSpace(rest))
				if ip == nil {
					return nil, nil, nil, nil, fmt.Errorf("subject alternative name %q is not an IP address", rest)
				}
				ips = append(ips, ip)
				continue
			case "EMAIL":
				emails = append(emails, strings.TrimSpace(rest))
				continue
			case "URI":
				u, perr := url.Parse(strings.TrimSpace(rest))
				if perr != nil {
					return nil, nil, nil, nil, fmt.Errorf("subject alternative name %q is not a URI: %w", rest, perr)
				}
				uris = append(uris, u)
				continue
			}
		}
		// No prefix. An address is an IP, an at sign is an email, and
		// anything else is a DNS name.
		if ip := net.ParseIP(e); ip != nil {
			ips = append(ips, ip)
			continue
		}
		if strings.Contains(e, "@") {
			emails = append(emails, e)
			continue
		}
		dns = append(dns, e)
	}
	return dns, ips, emails, uris, nil
}

// parseKeyUsage reads the key usage names a tree writes.
var keyUsageNames = map[string]x509.KeyUsage{
	"digitalsignature":  x509.KeyUsageDigitalSignature,
	"contentcommitment": x509.KeyUsageContentCommitment,
	"nonrepudiation":    x509.KeyUsageContentCommitment,
	"keyencipherment":   x509.KeyUsageKeyEncipherment,
	"dataencipherment":  x509.KeyUsageDataEncipherment,
	"keyagreement":      x509.KeyUsageKeyAgreement,
	"keycertsign":       x509.KeyUsageCertSign,
	"crlsign":           x509.KeyUsageCRLSign,
	"encipheronly":      x509.KeyUsageEncipherOnly,
	"decipheronly":      x509.KeyUsageDecipherOnly,
}

var extKeyUsageNames = map[string]x509.ExtKeyUsage{
	"serverauth":      x509.ExtKeyUsageServerAuth,
	"clientauth":      x509.ExtKeyUsageClientAuth,
	"codesigning":     x509.ExtKeyUsageCodeSigning,
	"emailprotection": x509.ExtKeyUsageEmailProtection,
	"timestamping":    x509.ExtKeyUsageTimeStamping,
	"ocspsigning":     x509.ExtKeyUsageOCSPSigning,
	"any":             x509.ExtKeyUsageAny,
}

func parseKeyUsage(names []string) (x509.KeyUsage, error) {
	var out x509.KeyUsage
	for _, n := range names {
		u, ok := keyUsageNames[normaliseUsage(n)]
		if !ok {
			return 0, fmt.Errorf("unknown key usage %q", n)
		}
		out |= u
	}
	return out, nil
}

func parseExtKeyUsage(names []string) ([]x509.ExtKeyUsage, error) {
	var out []x509.ExtKeyUsage
	for _, n := range names {
		u, ok := extKeyUsageNames[normaliseUsage(n)]
		if !ok {
			return nil, fmt.Errorf("unknown extended key usage %q", n)
		}
		out = append(out, u)
	}
	return out, nil
}

// normaliseUsage accepts the several spellings a tree may use:
// "digitalSignature", "digital_signature", and "Digital Signature".
func normaliseUsage(s string) string {
	r := strings.NewReplacer("_", "", "-", "", " ", "")
	return strings.ToLower(r.Replace(strings.TrimSpace(s)))
}

// serialNumber draws a random 128-bit serial, which is what the CA/Browser
// Forum requires and what makes a serial unguessable.
func serialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	// A zero serial is legal but useless; nudge it to one.
	if n.Sign() == 0 {
		return big.NewInt(1), nil
	}
	return n, nil
}

// certFields renders a parsed certificate for a template or a report.
func certFields(c *x509.Certificate) *value.Map {
	m := value.NewMap(16)
	m.Set("subject", c.Subject.String())
	m.Set("issuer", c.Issuer.String())
	m.Set("common_name", c.Subject.CommonName)
	m.Set("serial_number", c.SerialNumber.String())
	m.Set("not_before", c.NotBefore.UTC().Format(time.RFC3339))
	m.Set("not_after", c.NotAfter.UTC().Format(time.RFC3339))
	m.Set("is_ca", c.IsCA)
	m.Set("signature_algorithm", c.SignatureAlgorithm.String())
	m.Set("public_key_algorithm", c.PublicKeyAlgorithm.String())
	m.Set("self_signed", c.Subject.String() == c.Issuer.String())

	names := make([]any, 0, len(c.DNSNames)+len(c.IPAddresses)+len(c.EmailAddresses))
	for _, d := range c.DNSNames {
		names = append(names, "DNS:"+d)
	}
	for _, ip := range c.IPAddresses {
		names = append(names, "IP:"+ip.String())
	}
	for _, e := range c.EmailAddresses {
		names = append(names, "email:"+e)
	}
	for _, u := range c.URIs {
		names = append(names, "URI:"+u.String())
	}
	m.Set("subject_alt_names", names)

	// The fingerprint is what an operator compares by eye, so it is
	// rendered the way every other tool renders it.
	m.Set("sha256_fingerprint", fingerprint(c.Raw))
	m.Set("days_remaining", int64(time.Until(c.NotAfter)/(24*time.Hour)))
	return m
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
