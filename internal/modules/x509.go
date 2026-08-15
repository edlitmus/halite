package modules

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func init() {
	register("x509.private_key_managed", x509PrivateKeyManaged)
	register("x509.certificate_managed", x509CertificateManaged)
}

// keyFileMode is what a private key is written with. A key another account
// can read is a compromised key, so this is the default rather than an
// argument's default.
const keyFileMode = 0o600

// x509PrivateKeyManaged ensures a private key exists.
//
//	/usr/local/etc/ssl/site.key:
//	  x509.private_key_managed:
//	    - algo: ec
//
// An existing, readable key of the right kind is left alone. Rotating a
// key invalidates every certificate signed from it, so it happens only
// when the state says `new: true`.
func x509PrivateKeyManaged(c *Ctx, id string, args map[string]any) Result {
	path := Str(args, "name", id)
	algo := strings.ToLower(Str(args, "algo", "ec"))
	bits, err := keyBits(args, algo)
	if err != nil {
		return resFail("%v", err)
	}

	reason := ""
	switch key, err := loadPrivateKey(path); {
	case Bool(args, "new", false):
		reason = "new: true"
	case err != nil:
		reason = "missing or unreadable"
	case !keyMatchesAlgo(key, algo, bits):
		reason = fmt.Sprintf("not %s", keyDescription(algo, bits))
	default:
		if err := applyKeyPermissions(c, path, args); err != nil {
			return resFail("%v", err)
		}
		return resOK(fmt.Sprintf("%s is a %s private key", path, keyDescription(algo, bits)))
	}

	if c.Test {
		return resWould(fmt.Sprintf("%s would be generated (%s)", path, reason))
	}
	keyPEM, err := generatePrivateKey(algo, bits)
	if err != nil {
		return resFail("%v", err)
	}
	if err := writeSecret(path, keyPEM, args); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("%s generated (%s)", path, keyDescription(algo, bits)),
		map[string]string{path: "generated: " + reason})
}

// keyBits reads the RSA size. EC keys are P-256, matching the fleet CA:
// small certificates, and every TLS 1.2+ stack takes them.
func keyBits(args map[string]any, algo string) (int, error) {
	raw := Str(args, "bits", "")
	if algo == "ec" {
		if raw != "" {
			return 0, fmt.Errorf("bits applies to rsa keys; ec keys are P-256")
		}
		return 0, nil
	}
	if algo != "rsa" {
		return 0, fmt.Errorf("algo %q is not ec or rsa", algo)
	}
	if raw == "" {
		return 2048, nil
	}
	bits, err := strconv.Atoi(raw)
	if err != nil || bits < 2048 {
		return 0, fmt.Errorf("bits %q is not a number of at least 2048", raw)
	}
	return bits, nil
}

func keyDescription(algo string, bits int) string {
	if algo == "ec" {
		return "P-256 ec"
	}
	return fmt.Sprintf("%d-bit rsa", bits)
}

func generatePrivateKey(algo string, bits int) ([]byte, error) {
	var key crypto.Signer
	var err error
	if algo == "ec" {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	} else {
		key, err = rsa.GenerateKey(rand.Reader, bits)
	}
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// loadPrivateKey reads a PEM private key in any of the three encodings
// other tools write.
func loadPrivateKey(path string) (crypto.Signer, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, fmt.Errorf("%s is not PEM", path)
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("%s is not a signing key", path)
		}
		return signer, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: unrecognised private key", path)
	}
	return key, nil
}

func keyMatchesAlgo(key crypto.Signer, algo string, bits int) bool {
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		return algo == "ec" && k.Curve == elliptic.P256()
	case *rsa.PrivateKey:
		return algo == "rsa" && k.N.BitLen() == bits
	}
	return false
}

// writeSecret writes a private key with a mode that does not widen, then
// applies any ownership the state asked for.
func writeSecret(path string, body []byte, args map[string]any) error {
	mode := os.FileMode(keyFileMode)
	if explicit, err := parseModeArg(args, "mode"); err != nil {
		return err
	} else if explicit != 0 {
		mode = explicit
	}
	if Bool(args, "makedirs", true) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
	}
	if err := atomicWrite(path, body, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return applyEditOwner(path, args)
}

// applyKeyPermissions repairs the mode and ownership of a key that is
// otherwise correct — a key left group-readable is worth fixing without
// rotating it.
func applyKeyPermissions(c *Ctx, path string, args map[string]any) error {
	want := os.FileMode(keyFileMode)
	if explicit, err := parseModeArg(args, "mode"); err != nil {
		return err
	} else if explicit != 0 {
		want = explicit
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() == want || c.Test {
		return nil
	}
	if err := setMode(path, want, Bool(args, FollowSymlinksArg, false)); err != nil {
		return err
	}
	return applyEditOwner(path, args)
}

// x509CertificateManaged ensures a certificate exists, matches what the
// state says, and is not about to expire.
//
//	/usr/local/etc/ssl/site.crt:
//	  x509.certificate_managed:
//	    - private_key: /usr/local/etc/ssl/site.key
//	    - CN: site.example.com
//	    - subject_alt_names:
//	      - DNS:site.example.com
//	      - IP:10.0.0.5
//	    - days_valid: 365
//	    - days_remaining: 28
//
// `ca: true` issues a certificate that signs others rather than one that
// serves TLS — an internal CA is two states, a key and this.
//
// With `signing_private_key` and `signing_cert` it is signed by that CA;
// without them it is self-signed. Renewal is the point of `days_remaining`:
// a certificate inside that window is reissued on the next run, so a
// converging fleet renews itself.
func x509CertificateManaged(c *Ctx, id string, args map[string]any) Result {
	path := Str(args, "name", id)
	keyPath := Str(args, "private_key", "")
	if keyPath == "" {
		return resFail("x509.certificate_managed needs private_key")
	}
	key, err := loadPrivateKey(keyPath)
	if err != nil {
		return resFail("private_key %s: %v", keyPath, err)
	}

	wanted, err := certificateTemplate(id, args)
	if err != nil {
		return resFail("%v", err)
	}
	daysRemaining, err := intArg(args, "days_remaining", 28)
	if err != nil {
		return resFail("%v", err)
	}

	reason := certificateDrift(path, key, wanted, time.Duration(daysRemaining)*24*time.Hour)
	if reason == "" {
		return resOK(fmt.Sprintf("%s is current (expires %s)",
			path, certificateExpiry(path).Format(time.DateOnly)))
	}
	if c.Test {
		return resWould(fmt.Sprintf("%s would be issued (%s)", path, reason))
	}

	certPEM, err := issueCertificate(wanted, key, args)
	if err != nil {
		return resFail("%v", err)
	}
	mode := os.FileMode(0o644)
	if explicit, err := parseModeArg(args, "mode"); err != nil {
		return resFail("%v", err)
	} else if explicit != 0 {
		mode = explicit
	}
	if Bool(args, "makedirs", true) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return resFail("mkdir %s: %v", filepath.Dir(path), err)
		}
	}
	if err := atomicWrite(path, certPEM, mode); err != nil {
		return resFail("write %s: %v", path, err)
	}
	if err := applyEditOwner(path, args); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("%s issued (%s)", path, reason),
		map[string]string{path: "issued: " + reason, "expires": wanted.NotAfter.Format(time.DateOnly)})
}

// certificateTemplate builds what the certificate should say.
func certificateTemplate(id string, args map[string]any) (*x509.Certificate, error) {
	commonName := Str(args, "CN", Str(args, "cn", ""))
	if commonName == "" {
		commonName = filepath.Base(Str(args, "name", id))
	}
	days, err := intArg(args, "days_valid", 365)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		Subject: pkix.Name{
			CommonName:         commonName,
			Organization:       nonEmpty(Str(args, "O", "")),
			OrganizationalUnit: nonEmpty(Str(args, "OU", "")),
			Country:            nonEmpty(Str(args, "C", "")),
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if Bool(args, "ca", false) {
		// A certificate that signs others is a different thing from one
		// that serves TLS, and nothing will accept it as both.
		tmpl.IsCA = true
		tmpl.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		tmpl.ExtKeyUsage = nil
	}
	for _, san := range List(args, "subject_alt_names") {
		kind, value, found := strings.Cut(san, ":")
		if !found {
			// A bare name is a DNS name, which is what it almost always is.
			kind, value = "DNS", san
		}
		switch strings.ToUpper(strings.TrimSpace(kind)) {
		case "DNS":
			tmpl.DNSNames = append(tmpl.DNSNames, strings.TrimSpace(value))
		case "IP":
			ip := net.ParseIP(strings.TrimSpace(value))
			if ip == nil {
				return nil, fmt.Errorf("subject_alt_names: %q is not an IP address", value)
			}
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		case "EMAIL":
			tmpl.EmailAddresses = append(tmpl.EmailAddresses, strings.TrimSpace(value))
		default:
			return nil, fmt.Errorf("subject_alt_names: %q is not DNS:, IP:, or email:", san)
		}
	}
	if !tmpl.IsCA && len(tmpl.DNSNames) == 0 && len(tmpl.IPAddresses) == 0 {
		// A server certificate with no SAN is not accepted by anything
		// modern, so the common name stands in rather than issuing
		// something unusable. A CA is identified by its subject, not a SAN.
		tmpl.DNSNames = []string{commonName}
	}
	return tmpl, nil
}

// certificateDrift returns why the certificate has to be issued, or "" if
// it does not.
func certificateDrift(path string, key crypto.Signer, wanted *x509.Certificate, window time.Duration) string {
	current, err := loadCertificate(path)
	if err != nil {
		return "missing or unreadable"
	}
	if time.Until(current.NotAfter) < window {
		return fmt.Sprintf("expires %s, inside the renewal window", current.NotAfter.Format(time.DateOnly))
	}
	if !samePublicKey(current.PublicKey, key.Public()) {
		return "does not match its private key"
	}
	if current.Subject.CommonName != wanted.Subject.CommonName {
		return fmt.Sprintf("common name is %q, wanted %q", current.Subject.CommonName, wanted.Subject.CommonName)
	}
	if !sameStrings(current.DNSNames, wanted.DNSNames) || !sameIPs(current.IPAddresses, wanted.IPAddresses) {
		return "subject alternative names differ"
	}
	if current.IsCA != wanted.IsCA {
		return "certificate authority flag differs"
	}
	return ""
}

// issueCertificate signs the template, with a CA when the state names one
// and self-signed when it does not.
func issueCertificate(tmpl *x509.Certificate, key crypto.Signer, args map[string]any) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}
	tmpl.SerialNumber = serial

	parent, parentKey := tmpl, key
	signingKeyPath := Str(args, "signing_private_key", "")
	if signingKeyPath != "" {
		signingKey, err := loadPrivateKey(signingKeyPath)
		if err != nil {
			return nil, fmt.Errorf("signing_private_key %s: %w", signingKeyPath, err)
		}
		signingCertPath := Str(args, "signing_cert", "")
		if signingCertPath == "" {
			return nil, fmt.Errorf("signing_private_key needs signing_cert")
		}
		signingCert, err := loadCertificate(signingCertPath)
		if err != nil {
			return nil, fmt.Errorf("signing_cert %s: %w", signingCertPath, err)
		}
		parent, parentKey = signingCert, signingKey
	} else {
		// A self-signed certificate signs itself, so it needs the subject
		// as its own issuer and the key as its own parent key.
		tmpl.Issuer = tmpl.Subject
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, key.Public(), parentKey)
	if err != nil {
		return nil, fmt.Errorf("sign certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func loadCertificate(path string) (*x509.Certificate, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, fmt.Errorf("%s is not PEM", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

// certificateExpiry is the NotAfter of the certificate on disk, or the zero
// time when there is not one.
func certificateExpiry(path string) time.Time {
	cert, err := loadCertificate(path)
	if err != nil {
		return time.Time{}
	}
	return cert.NotAfter
}

func samePublicKey(a, b crypto.PublicKey) bool {
	aDER, errA := x509.MarshalPKIXPublicKey(a)
	bDER, errB := x509.MarshalPKIXPublicKey(b)
	return errA == nil && errB == nil && bytes.Equal(aDER, bDER)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameIPs(a, b []net.IP) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

func intArg(args map[string]any, key string, fallback int) (int, error) {
	raw := Str(args, key, "")
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s %q is not a positive number", key, raw)
	}
	return value, nil
}

func nonEmpty(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}
