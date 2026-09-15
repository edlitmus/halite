package builtin

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// keyParams are the algorithm arguments every key-producing function
// takes, so a tree writes them the same way wherever they appear.
// x509KeyParams is how the x509 modules spell a key, which is how Salt's
// x509_v2 spells it: `algo` and `keysize`.
//
// The tls module spells the same idea differently, and that is not an
// inconsistency to tidy away -- see tlsKeyParams.
func x509KeyParams() []signature.Param {
	return []signature.Param{
		choice("algo", "rsa", "The key algorithm.", "rsa", "ec", "ed25519"),
		opt("keysize", signature.Int, int64(0),
			"RSA bits, refused below 2048 and defaulting to 4096; or the EC curve size 256, 384 or 521, "+
				"defaulting to 256. Zero takes the algorithm's default, as Salt's None does."),
	}
}

// tlsKeyParams is how the tls module spells a key, which is how Salt's
// tls module spells it: `bits`.
//
// salt.modules.tls.create_ca takes `bits` and is RSA only; `algo`,
// `keysize` and `curve` appear nowhere in it, and create_ca_signed_cert
// takes no key argument at all. So a tree writing `bits:` to
// `tls.create_ca` and `keysize:` to `x509.private_key_managed` is writing
// correct Salt both times, and halite has to answer to both. Spelling
// these two the same would make one of them wrong.
//
// `algorithm` and `curve` are halite's own, for the EC keys Salt's tls
// cannot make at all.
func tlsKeyParams() []signature.Param {
	return []signature.Param{
		choice("algorithm", "rsa", "The key algorithm.", "rsa", "ec", "ed25519"),
		opt("bits", signature.Int, int64(4096), "RSA key size. Refused below 2048."),
		choice("curve", "p256", "The elliptic curve, when the algorithm is ec.", "p256", "p384", "p521"),
	}
}

func subjectParams() []signature.Param {
	return []signature.Param{
		opt("CN", signature.String, "", "Common name."),
		opt("O", signature.String, "", "Organization."),
		opt("OU", signature.String, "", "Organizational unit."),
		opt("C", signature.String, "", "Country, as a two-letter code."),
		opt("ST", signature.String, "", "State or province."),
		opt("L", signature.String, "", "Locality."),
		opt("subject_alt_names", signature.List, nil,
			"Subject alternative names. A bare entry is a DNS name; `IP:`, `email:`, and `URI:` name the others."),
	}
}

func x509KeySpecFrom(args *value.Map) (keySpec, error) {
	return parseKeySpec(
		states.Str(args, "algo", "rsa"),
		states.Int(args, "keysize", 0),
	)
}

// tlsKeySpecFrom reads tls's spelling onto the same spec. `curve` is a
// name here and a size in x509, so it is translated rather than passed.
func tlsKeySpecFrom(args *value.Map) (keySpec, error) {
	algorithm := states.Str(args, "algorithm", "rsa")
	keysize := states.Int(args, "bits", 4096)
	if strings.EqualFold(strings.TrimSpace(algorithm), "ec") {
		switch states.Str(args, "curve", "p256") {
		case "p256":
			keysize = 256
		case "p384":
			keysize = 384
		case "p521":
			keysize = 521
		default:
			return keySpec{}, fmt.Errorf("unknown curve %q; halite generates p256, p384, and p521",
				states.Str(args, "curve", ""))
		}
	}
	return parseKeySpec(algorithm, keysize)
}

func registerX509(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "create_private_key",
				Doc: "Generate a private key and write it, or return its PEM when no path is given.",
				Params: append([]signature.Param{
					opt("path", signature.Path, "", "Where to write it. Empty returns the PEM instead."),
				}, x509KeyParams()...),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				spec, err := x509KeySpecFrom(args)
				if err != nil {
					return nil, err
				}
				key, err := generateKey(spec)
				if err != nil {
					return nil, err
				}
				encoded, err := encodePrivateKey(key)
				if err != nil {
					return nil, err
				}
				path := states.Str(args, "path", "")
				if path == "" {
					return string(encoded), nil
				}
				// A private key is 0600 and nothing else. Writing it
				// world-readable for even the moment between create and
				// chmod is how a key ends up in a backup it should not be
				// in, which is why writeAtomic sets the mode before the
				// rename.
				if err := writeAtomic(path, encoded, 0o600); err != nil {
					return nil, err
				}
				return path, nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "create_csr",
				Doc: "Create a certificate signing request from an existing private key.",
				Params: append([]signature.Param{
					req("private_key", signature.String, "The signing key, as a path or as PEM."),
					opt("path", signature.Path, "", "Where to write the CSR. Empty returns the PEM."),
				}, subjectParams()...),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				key, err := loadPrivateKey(states.Str(args, "private_key", ""))
				if err != nil {
					return nil, err
				}
				dns, ips, emails, uris, err := parseSANs(states.Strings(args, "subject_alt_names"))
				if err != nil {
					return nil, err
				}
				tmpl := &x509.CertificateRequest{
					Subject:        buildSubject(args),
					DNSNames:       dns,
					IPAddresses:    ips,
					EmailAddresses: emails,
					URIs:           uris,
				}
				der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
				if err != nil {
					return nil, err
				}
				encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
				path := states.Str(args, "path", "")
				if path == "" {
					return string(encoded), nil
				}
				if err := writeAtomic(path, encoded, 0o644); err != nil {
					return nil, err
				}
				return path, nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "create_certificate",
				Doc: "Create a certificate, self-signed or signed by a CA.",
				Params: append([]signature.Param{
					opt("private_key", signature.String, "", "The subject's key, as a path or as PEM. Its public half goes into the certificate."),
					opt("public_key", signature.String, "", "The subject's public key, when this node holds no private half of it."),
					opt("path", signature.Path, "", "Where to write it. Empty returns the PEM."),
					opt("signing_cert", signature.String, "", "The CA certificate. Empty makes the certificate self-signed."),
					opt("signing_private_key", signature.String, "", "The key that signs. Also the subject's key when nothing else names one."),
					opt("days_valid", signature.Int, int64(defaultCertDays), "How long it lasts."),
					opt("ca", signature.Bool, false, "Mark it a CA, with basic constraints and a path length of zero."),
					opt("key_usage", signature.List, nil, "Key usages, such as digitalSignature and keyEncipherment."),
					opt("ext_key_usage", signature.List, nil, "Extended key usages, such as serverAuth."),
				}, append(certExtensionParams(), subjectParams()...)...),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return createCertificate(args, states.Str(args, "path", ""), 0)
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "read_certificate",
				Doc: "Return a certificate's fields.",
				Params: []signature.Param{
					req("certificate", signature.String, "A path or PEM text."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				cert, err := loadCertificate(states.Str(args, "certificate", ""))
				if err != nil {
					return nil, err
				}
				return certFields(cert), nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "read_csr",
				Doc: "Return a certificate signing request's fields.",
				Params: []signature.Param{
					req("csr", signature.String, "A path or PEM text."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				csr, err := loadCSR(states.Str(args, "csr", ""))
				if err != nil {
					return nil, err
				}
				m := value.NewMap(6)
				m.Set("subject", csr.Subject.String())
				m.Set("common_name", csr.Subject.CommonName)
				m.Set("public_key_algorithm", csr.PublicKeyAlgorithm.String())
				m.Set("signature_algorithm", csr.SignatureAlgorithm.String())
				names := make([]any, 0, len(csr.DNSNames))
				for _, d := range csr.DNSNames {
					names = append(names, "DNS:"+d)
				}
				for _, ip := range csr.IPAddresses {
					names = append(names, "IP:"+ip.String())
				}
				m.Set("subject_alt_names", names)
				return m, nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "get_public_key",
				Doc: "Return the public key of a private key, a certificate, or a CSR, as PEM.",
				Params: []signature.Param{
					req("source", signature.String, "A path or PEM text."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				pub, err := publicKeyOf(states.Str(args, "source", ""))
				if err != nil {
					return nil, err
				}
				der, err := x509.MarshalPKIXPublicKey(pub)
				if err != nil {
					return nil, err
				}
				return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "expires",
				Doc: "Report whether a certificate expires within a window.",
				Params: []signature.Param{
					req("certificate", signature.String, "A path or PEM text."),
					opt("days", signature.Int, int64(30), "The window."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				cert, err := loadCertificate(states.Str(args, "certificate", ""))
				if err != nil {
					return nil, err
				}
				window := time.Duration(states.Int(args, "days", 30)) * 24 * time.Hour
				return time.Now().Add(window).After(cert.NotAfter), nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "verify_signature",
				Doc: "Report whether a certificate was signed by a given CA certificate.",
				Params: []signature.Param{
					req("certificate", signature.String, "A path or PEM text."),
					req("signing_cert", signature.String, "The CA certificate, as a path or PEM."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				cert, err := loadCertificate(states.Str(args, "certificate", ""))
				if err != nil {
					return nil, err
				}
				ca, err := loadCertificate(states.Str(args, "signing_cert", ""))
				if err != nil {
					return nil, err
				}
				// The signature is checked, not the chain: a caller asking
				// this question wants to know who signed the certificate,
				// and a chain check would also fail on an expiry, which is
				// a different question with its own function.
				return cert.CheckSignatureFrom(ca) == nil, nil
			},
		},
	)
}

func loadCSR(source string) (*x509.CertificateRequest, error) {
	data, err := pemSource(source)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in the certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	// A CSR carries its own signature, and a request whose signature does
	// not check is not a request: it is a name someone else chose.
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("the certificate request's signature does not verify: %w", err)
	}
	return csr, nil
}

// publicKeyOf accepts any of the three things that carry a public key, so
// a tree can compare a key against a certificate without knowing which it
// has in hand.
func publicKeyOf(source string) (crypto.PublicKey, error) {
	data, err := pemSource(source)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	switch {
	case strings.Contains(block.Type, "PRIVATE KEY"):
		key, err := loadPrivateKey(string(data))
		if err != nil {
			return nil, err
		}
		return key.Public(), nil
	case block.Type == "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		return cert.PublicKey, nil
	case block.Type == "CERTIFICATE REQUEST":
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			return nil, err
		}
		return csr.PublicKey, nil
	case block.Type == "PUBLIC KEY":
		return x509.ParsePKIXPublicKey(block.Bytes)
	}
	return nil, fmt.Errorf("a %s block carries no public key", block.Type)
}

// createCertificate builds a certificate from the arguments both the
// module function and the state use.
//
// The destination is a parameter rather than an argument name, because
// the two callers spell it differently: the module function takes `path`
// and the state takes `name`. Reading it from the arguments meant the
// state wrote nowhere and returned the PEM to a caller that discarded it,
// so the certificate was created afresh on every run and never appeared
// on disk. An empty path returns the PEM.
func createCertificate(args *value.Map, path string, mode os.FileMode) (any, error) {
	subjectPub, subjectKey, err := resolveSubjectKey(args)
	if err != nil {
		return nil, err
	}

	days := states.Int(args, "days_valid", defaultCertDays)
	if days <= 0 {
		return nil, fmt.Errorf("days_valid must be positive, found %d", days)
	}

	// The SANs a tree gives as a list, plus the ones it gives as Salt's
	// single `subjectAltName` string. Both are read, because a tree that
	// came from Salt writes the second and one written for this build
	// writes the first.
	sanEntries := states.Strings(args, "subject_alt_names")
	if s := states.Str(args, "subjectAltName", ""); s != "" {
		sanEntries = append(sanEntries, splitSANString(s)...)
	}
	dns, ips, emails, uris, err := parseSANs(sanEntries)
	if err != nil {
		return nil, err
	}

	usage, err := parseKeyUsage(states.Strings(args, "key_usage"))
	if err != nil {
		return nil, err
	}
	if s := states.Str(args, "keyUsage", ""); s != "" {
		u, critical, err := parseKeyUsageString(s)
		if err != nil {
			return nil, err
		}
		if !critical {
			// Go writes the key usage extension critical and offers no
			// way to write it otherwise. Refused rather than written
			// critical anyway, because a certificate that differs from
			// what the tree asked for is the one thing worse than a
			// certificate that was not issued.
			return nil, fmt.Errorf("keyUsage must be marked critical; this build writes that extension " +
				"critical, as RFC 5280 requires when it is present, and cannot write it any other way")
		}
		usage |= u
	}

	extUsage, err := parseExtKeyUsage(states.Strings(args, "ext_key_usage"))
	if err != nil {
		return nil, err
	}
	if s := states.Str(args, "extendedKeyUsage", ""); s != "" {
		e, critical, err := parseExtKeyUsageString(s)
		if err != nil {
			return nil, err
		}
		if critical {
			return nil, fmt.Errorf("extendedKeyUsage cannot be marked critical here; this build writes " +
				"that extension non-critical and cannot write it any other way")
		}
		extUsage = append(extUsage, e...)
	}

	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}

	isCA := states.Bool(args, "ca", false)
	pathLen, hasPathLen := 0, false
	if s := states.Str(args, "basicConstraints", ""); s != "" {
		bc, err := parseBasicConstraints(s)
		if err != nil {
			return nil, err
		}
		if !bc.Critical {
			return nil, fmt.Errorf("basicConstraints must be marked critical; this build writes that " +
				"extension critical and cannot write it any other way")
		}
		isCA, pathLen, hasPathLen = bc.IsCA, bc.PathLen, bc.HasPath
	}

	if usage == 0 {
		// A certificate with no key usage is usable for everything, which
		// no tree means. The default follows what the certificate is for.
		if isCA {
			usage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature
		} else {
			usage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
		}
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      buildSubject(args),
		// One minute of backdating, because a node whose clock is a few
		// seconds ahead of the CA's would otherwise reject a certificate
		// the moment it is issued.
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage:              usage,
		ExtKeyUsage:           extUsage,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		DNSNames:              dns,
		IPAddresses:           ips,
		EmailAddresses:        emails,
		URIs:                  uris,
	}
	switch {
	case !isCA:
	case hasPathLen:
		// What basicConstraints actually said. Salt writes pathlen
		// through, and a CA issued with a path length it did not ask for
		// cannot sign the intermediates the tree expects it to.
		tmpl.MaxPathLen = pathLen
		tmpl.MaxPathLenZero = pathLen == 0
	default:
		// No pathlen named: a leaf-signing CA, which is what `ca: true`
		// has always meant here.
		tmpl.MaxPathLen = 0
		tmpl.MaxPathLenZero = true
	}

	if ski := states.Str(args, "subjectKeyIdentifier", ""); ski != "" {
		if strings.Contains(strings.ToLower(ski), "critical") {
			// Salt raises on this too: RFC 5280 says the extension is
			// always non-critical.
			return nil, fmt.Errorf("subjectKeyIdentifier must be non-critical")
		}
		if !strings.EqualFold(strings.TrimSpace(ski), "hash") {
			return nil, fmt.Errorf("subjectKeyIdentifier must be \"hash\", found %q", ski)
		}
		id, err := subjectKeyID(subjectPub)
		if err != nil {
			return nil, fmt.Errorf("the subject key identifier could not be computed: %w", err)
		}
		tmpl.SubjectKeyId = id
	}

	parent := tmpl
	signer := subjectKey
	if caPEM := states.Str(args, "signing_cert", ""); caPEM != "" {
		ca, err := loadCertificate(caPEM)
		if err != nil {
			return nil, fmt.Errorf("the signing certificate: %w", err)
		}
		if !ca.IsCA {
			return nil, fmt.Errorf("the signing certificate is not a CA; its basic constraints do not permit issuing")
		}
		parent = ca
		caKey := states.Str(args, "signing_private_key", "")
		if caKey == "" {
			return nil, fmt.Errorf("signing_cert was given without signing_private_key; the CA cannot sign without its key")
		}
		signer, err = loadPrivateKey(caKey)
		if err != nil {
			return nil, fmt.Errorf("the signing private key: %w", err)
		}
	} else if caKey := states.Str(args, "signing_private_key", ""); caKey != "" {
		// A signing key with no signing certificate. The result is
		// self-issued rather than CA-issued, and the key that signs it is
		// the one the tree named -- which is only the subject's key when
		// the tree gave no other.
		signer, err = loadPrivateKey(caKey)
		if err != nil {
			return nil, fmt.Errorf("the signing private key: %w", err)
		}
	}

	if signer == nil {
		return nil, fmt.Errorf("this certificate names a public key but no key to sign with; " +
			"give signing_private_key, or private_key for a self-signed certificate")
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, subjectPub, signer)
	if err != nil {
		return nil, err
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	if path == "" {
		return string(encoded), nil
	}
	if mode == 0 {
		mode = 0o644
	}
	if err := writeAtomic(path, encoded, mode); err != nil {
		return nil, err
	}
	return path, nil
}

var _ = pkix.Name{}
