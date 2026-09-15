package builtin

import (
	"crypto"
	"crypto/sha1"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// Salt's spelling of the X.509 extensions, which is OpenSSL's.
//
// A tree does not write `key_usage: [digitalSignature]`. It writes what
// an openssl.cnf writes:
//
//	- basicConstraints: "critical, CA:false"
//	- keyUsage: "critical, digitalSignature, keyEncipherment"
//	- extendedKeyUsage: serverAuth
//	- subjectKeyIdentifier: hash
//	- subjectAltName: "DNS:localhost, IP:127.0.0.1"
//
// These are not parameters in Salt at all: `certificate_managed` takes
// **kwargs and hands anything it does not recognise to
// x509.create_certificate, which checks the name against CERT_EXTS in
// salt/utils/x509.py. Halite declares them, because a declared parameter
// is what its own documentation and argument checking are built on, and
// an undeclared one cannot be refused by name when it is misspelled.
//
// Every encoding below was read from salt/utils/x509.py and then checked
// against the certificate a real Salt on this host produced from the
// estate's own `shared/salt/api.sls` arguments. See TestSaltsExtension
// StringsProduceSaltsCertificate.

// openSSLConfString parses the one-line form OpenSSL configuration uses,
// as Salt's _deserialize_openssl_confstring does: an optional leading
// `critical`, then comma-separated `key:value` pairs, where a bare word
// is a key that is simply present.
func openSSLConfString(s string) (fields map[string]string, order []string, critical bool) {
	t := strings.TrimSpace(s)
	if rest, found := strings.CutPrefix(t, "critical"); found {
		critical = true
		t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ","))
	}
	fields = map[string]string{}
	for _, item := range strings.Split(t, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		k, v, hasValue := strings.Cut(item, ":")
		k = strings.TrimSpace(k)
		if !hasValue {
			v = "__present__"
		}
		if _, seen := fields[k]; !seen {
			order = append(order, k)
		}
		fields[k] = strings.TrimSpace(v)
	}
	return fields, order, critical
}

// basicConstraints is what `basicConstraints: "critical, CA:true,
// pathlen:1"` means.
type basicConstraints struct {
	IsCA     bool
	PathLen  int
	HasPath  bool
	Critical bool
}

func parseBasicConstraints(s string) (basicConstraints, error) {
	var bc basicConstraints
	// Salt lowercases the whole string before parsing this one, which is
	// why `CA:false` and `ca:FALSE` are the same thing.
	fields, _, critical := openSSLConfString(strings.ToLower(s))
	bc.Critical = critical

	ca, ok := fields["ca"]
	if !ok {
		return bc, fmt.Errorf("basicConstraints needs a CA setting, as in \"critical, CA:false\"; found %q", s)
	}
	switch ca {
	case "true":
		bc.IsCA = true
	case "false":
		bc.IsCA = false
	default:
		return bc, fmt.Errorf("basicConstraints CA must be true or false, found %q", ca)
	}

	if raw, ok := fields["pathlen"]; ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return bc, fmt.Errorf("basicConstraints pathlen must be a non-negative whole number, found %q", raw)
		}
		if !bc.IsCA {
			// OpenSSL and RFC 5280 both refuse this, and a tree that
			// wrote it has the CA flag the wrong way round.
			return bc, fmt.Errorf("basicConstraints has pathlen:%d with CA:false; only a CA can have a path length", n)
		}
		bc.PathLen, bc.HasPath = n, true
	}
	return bc, nil
}

// parseKeyUsageString reads `keyUsage`. Salt tests each name for
// membership of the string; this splits it, which rejects a misspelling
// that Salt would silently drop.
func parseKeyUsageString(s string) (x509.KeyUsage, bool, error) {
	var usage x509.KeyUsage
	critical := false
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.EqualFold(item, "critical") {
			critical = true
			continue
		}
		u, ok := keyUsageNames[normaliseUsage(item)]
		if !ok {
			return 0, false, fmt.Errorf("unknown key usage %q", item)
		}
		usage |= u
	}
	return usage, critical, nil
}

// parseExtKeyUsageString reads `extendedKeyUsage`, which is one name or a
// comma-separated list of them.
func parseExtKeyUsageString(s string) ([]x509.ExtKeyUsage, bool, error) {
	var out []x509.ExtKeyUsage
	critical := false
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.EqualFold(item, "critical") {
			critical = true
			continue
		}
		u, ok := extKeyUsageNames[normaliseUsage(item)]
		if !ok {
			return nil, false, fmt.Errorf("unknown extended key usage %q; halite knows "+
				"serverAuth, clientAuth, codeSigning, emailProtection, timeStamping, OCSPSigning, and any", item)
		}
		out = append(out, u)
	}
	return out, critical, nil
}

// subjectKeyID computes the identifier `subjectKeyIdentifier: hash`
// asks for: the SHA-1 of the subjectPublicKey BIT STRING, which is
// RFC 5280 section 4.2.1.2's first method, what python-cryptography's
// SubjectKeyIdentifier.from_public_key produces, and what Go's own x509
// puts in a CA certificate.
//
// SHA-1 is not a security property here. It is an index into a chain,
// specified as SHA-1, and a different digest would name the key
// differently from every other tool that reads the certificate.
func subjectKeyID(pub crypto.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	var spki struct {
		Algorithm     asn1.RawValue
		SubjectPubKey asn1.BitString
	}
	if _, err := asn1.Unmarshal(der, &spki); err != nil {
		return nil, err
	}
	sum := sha1.Sum(spki.SubjectPubKey.Bytes)
	return sum[:], nil
}

// splitSANString turns `subjectAltName: "DNS:localhost, IP:127.0.0.1"`
// into the entries parseSANs already reads. The per-entry syntax is the
// same in both forms, so only the comma is new.
func splitSANString(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// resolveSubjectKey answers which key the certificate is *for*.
//
// Salt's certificate_managed has no required key argument: public_key,
// private_key, signing_private_key and csr are alternatives
// (salt/states/x509_v2.py, and confirmed against the running Salt, which
// issued a self-signed certificate from signing_private_key alone). This
// build had private_key as required, so a tree doing what the estate's
// own shared/salt/api.sls does -- naming only signing_private_key --
// was told private_key was missing when it was not needed.
//
// The signer is separate and may be nil: a certificate can be built for
// a public key this node holds no private half of, as long as a CA key
// signs it.
func resolveSubjectKey(args *value.Map) (pub crypto.PublicKey, signer crypto.Signer, err error) {
	if src := states.Str(args, "private_key", ""); src != "" {
		key, err := loadPrivateKey(src)
		if err != nil {
			return nil, nil, fmt.Errorf("the subject's private key: %w", err)
		}
		return key.Public(), key, nil
	}
	if src := states.Str(args, "public_key", ""); src != "" {
		pub, err := loadPublicKey(src)
		if err != nil {
			return nil, nil, fmt.Errorf("the subject's public key: %w", err)
		}
		return pub, nil, nil
	}
	if src := states.Str(args, "signing_private_key", ""); src != "" {
		// Self-signed: the CA's key is also the subject's.
		key, err := loadPrivateKey(src)
		if err != nil {
			return nil, nil, fmt.Errorf("the signing private key: %w", err)
		}
		return key.Public(), key, nil
	}
	return nil, nil, fmt.Errorf("this certificate names no key; give private_key, public_key, or signing_private_key")
}

// loadPublicKey reads a PEM public key, or takes the public half of a
// PEM private key, from a path or from the text itself.
func loadPublicKey(source string) (crypto.PublicKey, error) {
	if key, err := loadPrivateKey(source); err == nil {
		return key.Public(), nil
	}
	data, err := pemSource(source)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a public key this build reads: %w", err)
	}
	return pub, nil
}

// certExtensionParams are the extension arguments a Salt tree writes, in
// Salt's spelling. They sit alongside this build's own `ca`, `key_usage`
// and `ext_key_usage`, which are read too: a tree written for halite
// keeps working, and a tree written for Salt starts working.
func certExtensionParams() []signature.Param {
	return []signature.Param{
		opt("basicConstraints", signature.String, "",
			"OpenSSL's form, as in \"critical, CA:false\" or \"critical, CA:true, pathlen:1\". Overrides `ca`."),
		opt("keyUsage", signature.String, "",
			"OpenSSL's form, as in \"critical, digitalSignature, keyEncipherment\"."),
		opt("extendedKeyUsage", signature.String, "", "One name or several, as in \"serverAuth, clientAuth\"."),
		opt("subjectKeyIdentifier", signature.String, "",
			"Only \"hash\", which is the SHA-1 of the subject public key that RFC 5280 specifies."),
		opt("subjectAltName", signature.String, "",
			"Salt's single-string form, as in \"DNS:localhost, IP:127.0.0.1\". Adds to `subject_alt_names`."),
	}
}
