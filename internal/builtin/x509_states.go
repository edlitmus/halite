package builtin

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The x509 states of SPEC section 15.5.
//
// Each answers one question before it does anything: is what is on disk
// already what the tree asked for? That is what makes the test-mode
// contract of 11.6 hold, and it is also what stops a highstate from
// re-issuing a certificate on every run — which is the failure people
// actually hit with Salt's x509, because a re-issued certificate has a
// new serial and a new expiry and so never converges.

func registerX509States(r *Registries) {
	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "private_key_managed",
				Doc: "Ensure a private key exists with the requested algorithm and size.",
				Params: append([]signature.Param{
					req("name", signature.Path, "Where the key lives."),
					opt("mode", signature.String, "0600", "The file mode. A key should not be readable by anyone else."),
					opt("new", signature.Bool, false, "Replace the key even when the existing one already matches."),
				}, keyParams()...),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: privateKeyManaged,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "certificate_managed",
				Doc: "Ensure a certificate exists, is signed by the expected CA, and is not close to expiry.",
				Params: append([]signature.Param{
					req("name", signature.Path, "Where the certificate lives."),
					req("private_key", signature.String, "The subject's key, as a path or PEM."),
					opt("signing_cert", signature.String, "", "The CA certificate. Empty means self-signed."),
					opt("signing_private_key", signature.String, "", "The CA's key."),
					opt("days_valid", signature.Int, int64(defaultCertDays), "How long a new certificate lasts."),
					opt("days_remaining", signature.Int, int64(30),
						"Re-issue when fewer than this many days remain. Zero re-issues only when the certificate is missing or wrong."),
					opt("ca", signature.Bool, false, "Mark it a CA."),
					opt("key_usage", signature.List, nil, "Key usages."),
					opt("ext_key_usage", signature.List, nil, "Extended key usages."),
					opt("mode", signature.String, "0644", "The file mode."),
				}, subjectParams()...),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: certificateManaged,
		},
	)
}

func privateKeyManaged(c *exec.Context, args *value.Map) (states.Result, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return states.False("This state needs a path."), nil
	}
	spec, err := keySpecFrom(args)
	if err != nil {
		return states.False(capitalizeFirst(err.Error()) + "."), nil
	}

	reason := ""
	switch existing, err := loadPrivateKey(path); {
	case os.IsNotExist(err):
		reason = "it does not exist"
	case err != nil:
		reason = "the existing file is not a private key halite reads"
	case states.Bool(args, "new", false):
		reason = "new was requested"
	default:
		if got := describeKey(existing); got != spec.describe() {
			reason = fmt.Sprintf("the existing key is %s, not %s", got, spec.describe())
		}
	}

	if reason == "" {
		return states.True(fmt.Sprintf("The %s private key at %s is already in place.", spec.describe(), path)), nil
	}

	changes := value.MapOf(path, states.Change(nil, spec.describe()))
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("A %s private key would be written to %s, because %s.", spec.describe(), path, reason),
			changes), nil
	}

	key, err := generateKey(spec)
	if err != nil {
		return states.False(fmt.Sprintf("The key could not be generated: %v", err)), nil
	}
	encoded, err := encodePrivateKey(key)
	if err != nil {
		return states.False(fmt.Sprintf("The key could not be encoded: %v", err)), nil
	}
	mode, err := parseMode(states.Str(args, "mode", "0600"))
	if err != nil {
		return states.False(capitalizeFirst(err.Error()) + "."), nil
	}
	if err := writeAtomic(path, encoded, mode); err != nil {
		return states.False(fmt.Sprintf("The key could not be written: %v", err)), nil
	}
	return states.Changed(
		fmt.Sprintf("A %s private key was written to %s, because %s.", spec.describe(), path, reason),
		changes), nil
}

func certificateManaged(c *exec.Context, args *value.Map) (states.Result, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return states.False("This state needs a path."), nil
	}
	window := states.Int(args, "days_remaining", 30)

	reason := ""
	var old any
	switch existing, err := loadCertificate(path); {
	case os.IsNotExist(err):
		reason = "it does not exist"
	case err != nil:
		reason = "the existing file is not a certificate halite reads"
	default:
		old = existing.NotAfter.UTC().Format(time.RFC3339)
		switch {
		case !publicKeyMatches(existing, args):
			reason = "it does not match the private key"
		case window > 0 && time.Now().Add(time.Duration(window)*24*time.Hour).After(existing.NotAfter):
			reason = fmt.Sprintf("it expires in under %d days, on %s", window, old)
		case !signerMatches(existing, args):
			reason = "it was not signed by the configured CA"
		}
	}

	if reason == "" {
		return states.True(fmt.Sprintf("The certificate at %s is already in place.", path)), nil
	}

	changes := value.MapOf(path, states.Change(old, "reissued"))
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("A certificate would be written to %s, because %s.", path, reason), changes), nil
	}

	mode, err := parseMode(states.Str(args, "mode", "0644"))
	if err != nil {
		return states.False(capitalizeFirst(err.Error()) + "."), nil
	}
	if _, err := createCertificate(args, path, mode); err != nil {
		return states.False(fmt.Sprintf("The certificate could not be created: %v", err)), nil
	}
	return states.Changed(
		fmt.Sprintf("A certificate was written to %s, because %s.", path, reason), changes), nil
}

// publicKeyMatches reports whether a certificate carries the public half
// of the configured private key. A certificate that does not is not the
// tree's certificate, whatever else is right about it.
func publicKeyMatches(cert *x509.Certificate, args *value.Map) bool {
	key, err := loadPrivateKey(states.Str(args, "private_key", ""))
	if err != nil {
		return false
	}
	return samePublicKey(cert.PublicKey, key.Public())
}

// signerMatches reports whether a certificate was signed by the CA the
// tree names. With no CA configured the question is whether it is
// self-signed.
func signerMatches(cert *x509.Certificate, args *value.Map) bool {
	caPEM := states.Str(args, "signing_cert", "")
	if caPEM == "" {
		// Self-signed. CheckSignatureFrom cannot answer this: it requires
		// the parent to be a CA, so a self-signed leaf fails it on the
		// basic constraints rather than on the signature. The question
		// here is whether the certificate signed itself, so the signature
		// is checked directly against its own key.
		if cert.Subject.String() != cert.Issuer.String() {
			return false
		}
		return cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil
	}
	ca, err := loadCertificate(caPEM)
	if err != nil {
		return false
	}
	return cert.CheckSignatureFrom(ca) == nil
}

func samePublicKey(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	if e, ok := a.(equaler); ok {
		return e.Equal(b)
	}
	return false
}

// describeKey names an existing key the way parseKeySpec names a
// requested one, so the two compare as strings.
func describeKey(key crypto.Signer) string {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return fmt.Sprintf("rsa %d", k.N.BitLen())
	case *ecdsa.PrivateKey:
		switch k.Curve.Params().BitSize {
		case 256:
			return "ec p256"
		case 384:
			return "ec p384"
		case 521:
			return "ec p521"
		}
		return "ec " + k.Curve.Params().Name
	case ed25519.PrivateKey:
		return "ed25519"
	}
	return "unknown"
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32*b2i(s[0] >= 'a' && s[0] <= 'z')) + s[1:]
}

func b2i(b bool) byte {
	if b {
		return 1
	}
	return 0
}
