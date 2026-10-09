package builtin

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"os"
	"sort"
	"strings"
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
					opt("user", signature.String, "", "The owner."),
					opt("group", signature.String, "", "The group."),
					opt("makedirs", signature.Bool, false, "Create the directory the file goes in, and any above it, when they are missing."),
					opt("dir_mode", signature.Mode, "", "Mode for directories makedirs creates. Empty takes the file's mode with the execute bit added to each digit that is not zero, as Salt does: 0600 makes 0700."),
				}, x509KeyParams()...),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: privateKeyManaged,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "x509", Function: "certificate_managed",
				Doc: "Ensure a certificate exists, is signed by the expected CA, carries the subject, names and usages asked for, and is not close to expiry.",
				Params: append([]signature.Param{
					req("name", signature.Path, "Where the certificate lives."),
					opt("private_key", signature.String, "", "The subject's key, as a path or PEM."),
					opt("public_key", signature.String, "", "The subject's public key, when this node holds no private half of it."),
					opt("signing_cert", signature.String, "", "The CA certificate. Empty means self-signed."),
					opt("signing_private_key", signature.String, "", "The key that signs. Also the subject's key when nothing else names one."),
					opt("days_valid", signature.Int, int64(defaultCertDays), "How long a new certificate lasts."),
					opt("days_remaining", signature.Int, int64(30),
						"Re-issue when fewer than this many days remain. Zero re-issues only when the certificate is missing or wrong."),
					opt("ca", signature.Bool, false, "Mark it a CA."),
					opt("key_usage", signature.List, nil, "Key usages."),
					opt("ext_key_usage", signature.List, nil, "Extended key usages."),
					opt("mode", signature.String, "0644", "The file mode."),
					opt("user", signature.String, "", "The owner."),
					opt("group", signature.String, "", "The group."),
					opt("makedirs", signature.Bool, false, "Create the directory the file goes in, and any above it, when they are missing."),
					opt("dir_mode", signature.Mode, "", "Mode for directories makedirs creates. Empty takes the file's mode with the execute bit added to each digit that is not zero, as Salt does: 0600 makes 0700."),
				}, append(certExtensionParams(), subjectParams()...)...),
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
	spec, err := x509KeySpecFrom(args)
	if err != nil {
		return states.False(capitalizeFirst(err.Error()) + "."), nil
	}

	reason := ""
	exists := true
	switch existing, err := loadPrivateKey(path); {
	case os.IsNotExist(err):
		reason, exists = "it does not exist", false
	case err != nil:
		reason = "the existing file is not a private key halite reads"
	case states.Bool(args, "new", false):
		reason = "new was requested"
	default:
		if got := describeKey(existing); got != spec.describe() {
			reason = fmt.Sprintf("the existing key is %s, not %s", got, spec.describe())
		}
	}

	wantUser, wantGroup := states.Str(args, "user", ""), states.Str(args, "group", "")
	ownerChange, ownerDiffers, err := plannedOwnership(path, exists, wantUser, wantGroup)
	if err != nil {
		return states.False(fmt.Sprintf("The ownership for %s could not be resolved: %v", path, err)), nil
	}

	if reason == "" && !ownerDiffers {
		return states.True(fmt.Sprintf("The %s private key at %s is already in place.", spec.describe(), path)), nil
	}

	// Ownership alone is fixed where it stands. Generating a new key
	// because the group was wrong would throw away the key every
	// certificate already issued against it depends on.
	if reason == "" {
		changes := value.MapOf("ownership", ownerChange)
		if c.Test {
			return states.WouldChange(
				fmt.Sprintf("The ownership of %s would be set to %s.", path, ownerLabel(wantUser, wantGroup)), changes), nil
		}
		if err := applyOwnership(path, wantUser, wantGroup); err != nil {
			return states.False(fmt.Sprintf("The ownership of %s could not be set: %v", path, err)), nil
		}
		return states.Changed(
			fmt.Sprintf("The ownership of %s was set to %s.", path, ownerLabel(wantUser, wantGroup)), changes), nil
	}

	changes := value.MapOf(path, states.Change(nil, spec.describe()))
	if ownerDiffers && ownerChange != nil {
		changes.Set("ownership", ownerChange)
	}
	mode, err := parseMode(states.Str(args, "mode", "0600"))
	if err != nil {
		return states.False(capitalizeFirst(err.Error()) + "."), nil
	}
	fail, made := prepareParent(c.Test, args, path, mode)
	if fail != nil {
		return *fail, nil
	}
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("A %s private key would be written to %s, because %s.%s", spec.describe(), path, reason, made),
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
	was := replacing(path)
	if err := writeAtomic(path, encoded, mode); err != nil {
		return states.False(fmt.Sprintf("The key could not be written: %v", err)), nil
	}
	warnings := keepReplacedOwner(path, was)
	if err := applyOwnership(path, wantUser, wantGroup); err != nil {
		return states.False(fmt.Sprintf("The key was written but its ownership could not be set: %v", err)), nil
	}
	return withWarnings(states.Changed(
		fmt.Sprintf("A %s private key was written to %s, because %s.%s", spec.describe(), path, reason, made),
		changes), warnings), nil
}

func certificateManaged(c *exec.Context, args *value.Map) (states.Result, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return states.False("This state needs a path."), nil
	}
	window := states.Int(args, "days_remaining", 30)

	reason := ""
	exists := true
	var old any
	switch existing, err := loadCertificate(path); {
	case os.IsNotExist(err):
		reason, exists = "it does not exist", false
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
		default:
			reason = requestedDiffers(existing, args)
		}
	}

	wantUser, wantGroup := states.Str(args, "user", ""), states.Str(args, "group", "")
	ownerChange, ownerDiffers, err := plannedOwnership(path, exists, wantUser, wantGroup)
	if err != nil {
		return states.False(fmt.Sprintf("The ownership for %s could not be resolved: %v", path, err)), nil
	}

	if reason == "" && !ownerDiffers {
		return states.True(fmt.Sprintf("The certificate at %s is already in place.", path)), nil
	}

	// Ownership alone never re-issues. A certificate gets a new serial
	// and a new expiry every time it is written, so re-issuing because
	// the group was wrong is a state that reports a change on every run
	// for as long as it is left in the tree -- the non-convergence this
	// whole file exists to avoid.
	if reason == "" {
		changes := value.MapOf("ownership", ownerChange)
		if c.Test {
			return states.WouldChange(
				fmt.Sprintf("The ownership of %s would be set to %s.", path, ownerLabel(wantUser, wantGroup)), changes), nil
		}
		if err := applyOwnership(path, wantUser, wantGroup); err != nil {
			return states.False(fmt.Sprintf("The ownership of %s could not be set: %v", path, err)), nil
		}
		return states.Changed(
			fmt.Sprintf("The ownership of %s was set to %s.", path, ownerLabel(wantUser, wantGroup)), changes), nil
	}

	changes := value.MapOf(path, states.Change(old, "reissued"))
	if ownerDiffers && ownerChange != nil {
		changes.Set("ownership", ownerChange)
	}
	mode, err := parseMode(states.Str(args, "mode", "0644"))
	if err != nil {
		return states.False(capitalizeFirst(err.Error()) + "."), nil
	}
	fail, made := prepareParent(c.Test, args, path, mode)
	if fail != nil {
		return *fail, nil
	}
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("A certificate would be written to %s, because %s.%s", path, reason, made), changes), nil
	}

	was := replacing(path)
	if _, err := createCertificate(args, path, mode); err != nil {
		return states.False(fmt.Sprintf("The certificate could not be created: %v", err)), nil
	}
	warnings := keepReplacedOwner(path, was)
	if err := applyOwnership(path, wantUser, wantGroup); err != nil {
		return states.False(fmt.Sprintf("The certificate was written but its ownership could not be set: %v", err)), nil
	}
	return withWarnings(states.Changed(
		fmt.Sprintf("A certificate was written to %s, because %s.%s", path, reason, made), changes), warnings), nil
}

// replacing is the file a rewrite is about to replace, or nil when there
// is none, for keepReplacedOwner.
func replacing(path string) os.FileInfo {
	info, err := os.Lstat(path)
	if err != nil {
		return nil
	}
	return info
}

// keepReplacedOwner gives a key or certificate that has just been
// rewritten the owner and group of the file it replaced, before any owner
// the state asks for is applied.
//
// Both states write through a temporary file and a rename, so the new
// file belongs to whoever ran the write, and only a requested `user` or
// `group` was ever put back. A certificate whose owner was set some other
// way -- by hand, or by a file state beside it -- went to root at its next
// renewal, every couple of months for the 90-day certificates
// docs/metrics.md describes, and a key a service reads as itself would
// then be unreadable to it. file.managed had the same defect and the same
// fix (DIVERGENCE 5.253); this is 5.255.
//
// A warning rather than a failure when it cannot be done, as there: an
// unprivileged run replacing another account's file is no worse off than
// before, and the certificate it wrote is still the one asked for.
func keepReplacedOwner(path string, was os.FileInfo) []string {
	if was == nil {
		return nil
	}
	if err := keepOwnership(path, was); err != nil {
		return []string{fmt.Sprintf("%s was rewritten and its previous owner could not be kept: %v", path, err)}
	}
	return nil
}

// requestedDiffers says how an existing certificate differs from the one
// the arguments ask for, or "" when it does not: its subject, its subject
// alternative names, its key usage and extended key usage, and whether it
// is a CA and with what path length. What it asks for is requestedTemplate,
// the same thing createCertificate signs, so a certificate this state has
// just written always compares equal to its own arguments and a second run
// changes nothing.
//
// Not the validity. Every issue has its own dates, and a certificate whose
// days_valid differed from the tree's would be reissued on every run; the
// renewal window is what governs the dates. Arguments that do not parse are
// not a difference here: the issue path reports them, as it always has.
func requestedDiffers(have *x509.Certificate, args *value.Map) string {
	want, err := requestedTemplate(args)
	if err != nil {
		return ""
	}
	if a, b := subjectFields(have.Subject), subjectFields(want.Subject); a != b {
		return fmt.Sprintf("its subject is %s and the state asks for %s", a, b)
	}
	if a, b := sortedStrings(sanStrings(have)), sortedStrings(sanStrings(want)); strings.Join(a, ", ") != strings.Join(b, ", ") {
		return fmt.Sprintf("its subject alternative names are [%s] and the state asks for [%s]",
			strings.Join(a, ", "), strings.Join(b, ", "))
	}
	if have.KeyUsage != want.KeyUsage {
		return fmt.Sprintf("its key usage is %d and the state asks for %d", have.KeyUsage, want.KeyUsage)
	}
	if a, b := sortedUsages(have.ExtKeyUsage), sortedUsages(want.ExtKeyUsage); a != b {
		return fmt.Sprintf("its extended key usage is [%s] and the state asks for [%s]", a, b)
	}
	if have.IsCA != want.IsCA {
		return fmt.Sprintf("it is%s a CA and the state asks for one that is%s", notIf(!have.IsCA), notIf(!want.IsCA))
	}
	if want.IsCA && (have.MaxPathLen != want.MaxPathLen || have.MaxPathLenZero != want.MaxPathLenZero) {
		return fmt.Sprintf("its path length is %d and the state asks for %d", have.MaxPathLen, want.MaxPathLen)
	}
	return ""
}

func subjectFields(n pkix.Name) string {
	return fmt.Sprintf("CN=%s C=%v O=%v OU=%v L=%v ST=%v",
		n.CommonName, n.Country, n.Organization, n.OrganizationalUnit, n.Locality, n.Province)
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sortedUsages(in []x509.ExtKeyUsage) string {
	names := make([]string, 0, len(in))
	for _, u := range in {
		names = append(names, extKeyUsageLabel(u))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// extKeyUsageLabel names a usage the way a tree spells it, for a comment
// an operator reads.
func extKeyUsageLabel(u x509.ExtKeyUsage) string {
	for name, v := range extKeyUsageNames {
		if v == u && name == normaliseUsage(name) {
			return name
		}
	}
	return fmt.Sprintf("%d", int(u))
}

func notIf(b bool) string {
	if b {
		return " not"
	}
	return ""
}

// publicKeyMatches reports whether a certificate carries the public half
// of the configured private key. A certificate that does not is not the
// tree's certificate, whatever else is right about it.
func publicKeyMatches(cert *x509.Certificate, args *value.Map) bool {
	pub, _, err := resolveSubjectKey(args)
	if err != nil {
		return false
	}
	return samePublicKey(cert.PublicKey, pub)
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
