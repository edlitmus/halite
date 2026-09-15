package builtin

import (
	"crypto/x509"
	"encoding/hex"
	"os"
	"os/user"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The arguments are copied from the estate's own shared/salt/api.sls,
// which is salt-api's TLS certificate -- the same job halite-api has.
//
// The expectations are not this build's opinion. A real Salt on this host
// was given exactly these arguments and the certificate it produced read:
//
//	X509v3 Basic Constraints: critical
//	    CA:FALSE
//	X509v3 Key Usage: critical
//	    Digital Signature, Key Encipherment
//	X509v3 Extended Key Usage:
//	    TLS Web Server Authentication
//	X509v3 Subject Key Identifier:
//	    85:1E:E7:5D:...            (SHA-1 of the subjectPublicKey bits)
//	X509v3 Subject Alternative Name:
//	    DNS:localhost, IP Address:127.0.0.1
func TestSaltsExtensionStringsProduceSaltsCertificate(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := dir + "/api.key"
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algo", "rsa", "keysize", int64(2048)))

	pemText := x509Call(t, r, "x509.create_certificate", value.MapOf(
		"signing_private_key", key,
		"CN", "localhost",
		"subjectAltName", "DNS:localhost, IP:127.0.0.1",
		"basicConstraints", "critical, CA:false",
		"keyUsage", "critical, digitalSignature, keyEncipherment",
		"extendedKeyUsage", "serverAuth",
		"subjectKeyIdentifier", "hash",
		"days_valid", int64(3650),
	)).(string)

	cert, err := loadCertificate(pemText)
	if err != nil {
		t.Fatalf("the certificate does not load back: %v", err)
	}

	// private_key was never given. Salt issues this self-signed from
	// signing_private_key alone, and so does this.
	if cert.Subject.CommonName != "localhost" || cert.Issuer.CommonName != "localhost" {
		t.Errorf("subject %q issuer %q; it should be self-signed as CN=localhost",
			cert.Subject.CommonName, cert.Issuer.CommonName)
	}

	if cert.IsCA {
		t.Error("CA:false was asked for and the certificate is a CA")
	}
	if !cert.BasicConstraintsValid {
		t.Error("basic constraints are not marked valid, so CA:false says nothing")
	}

	wantUsage := x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
	if cert.KeyUsage != wantUsage {
		t.Errorf("key usage = %b, want %b (digitalSignature, keyEncipherment)", cert.KeyUsage, wantUsage)
	}

	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("extended key usage = %v, want [serverAuth]", cert.ExtKeyUsage)
	}

	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "localhost" {
		t.Errorf("DNS names = %v, want [localhost]", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 1 || cert.IPAddresses[0].String() != "127.0.0.1" {
		t.Errorf("IP addresses = %v, want [127.0.0.1]", cert.IPAddresses)
	}

	// The identifier is the SHA-1 of the subjectPublicKey BIT STRING,
	// which is what every other tool that reads this chain computes.
	want, err := subjectKeyID(cert.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.SubjectKeyId) == 0 {
		t.Fatal("subjectKeyIdentifier: hash was asked for and no identifier was written")
	}
	if hex.EncodeToString(cert.SubjectKeyId) != hex.EncodeToString(want) {
		t.Errorf("subject key id = %x, want %x", cert.SubjectKeyId, want)
	}
}

// pathlen is written through rather than assumed. This build used to set
// a path length of zero on every CA, so a tree asking for an
// intermediate-signing CA silently got one that could sign only leaves.
func TestBasicConstraintsPathLenIsHonoured(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := dir + "/ca.key"
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algo", "ec"))

	for _, tc := range []struct {
		constraints string
		wantLen     int
		wantZero    bool
	}{
		{"critical, CA:true, pathlen:1", 1, false},
		{"critical, CA:true, pathlen:0", 0, true},
	} {
		pemText := x509Call(t, r, "x509.create_certificate", value.MapOf(
			"signing_private_key", key, "CN", "testca",
			"basicConstraints", tc.constraints,
			"keyUsage", "critical, keyCertSign, cRLSign",
		)).(string)
		cert, err := loadCertificate(pemText)
		if err != nil {
			t.Fatal(err)
		}
		if !cert.IsCA {
			t.Fatalf("%q did not produce a CA", tc.constraints)
		}
		if cert.MaxPathLen != tc.wantLen || cert.MaxPathLenZero != tc.wantZero {
			t.Errorf("%q produced MaxPathLen %d zero=%v, want %d zero=%v",
				tc.constraints, cert.MaxPathLen, cert.MaxPathLenZero, tc.wantLen, tc.wantZero)
		}
	}
}

// Salt's CERT_EXTS has seventeen names. This build writes five of them,
// and the other twelve have to be refused rather than dropped: an
// extension silently missing from a certificate is not a cosmetic
// difference, it is a different certificate.
func TestTheExtensionsThisBuildDoesNotWriteAreRefused(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := dir + "/k.key"
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algo", "ec"))

	for _, name := range []string{
		"authorityKeyIdentifier", "issuerAltName", "authorityInfoAccess",
		"crlDistributionPoints", "certificatePolicies", "policyConstraints",
		"inhibitAnyPolicy", "nameConstraints", "noCheck", "tlsfeature",
		"nsComment", "nsCertType",
	} {
		_, err := r.Exec.Call(newCtx(false), "x509.create_certificate",
			value.MapOf("signing_private_key", key, "CN", "x", name, "whatever"))
		if err == nil {
			t.Errorf("%s was accepted and silently left out of the certificate", name)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s was refused without naming it: %v", name, err)
		}
	}
}

// The certificate above cannot prove keyUsage is read, and that is worth
// saying out loud: this build's default for a non-CA is
// digitalSignature|keyEncipherment, which is exactly what api.sls asks
// for, so dropping the argument entirely leaves that test green. It was
// checked, and it was green.
//
// So this asks for a usage set the default would not produce.
func TestKeyUsageIsReadRatherThanDefaulted(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := dir + "/k.key"
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algo", "ec"))

	for _, tc := range []struct {
		usage string
		want  x509.KeyUsage
	}{
		// One bit, where the default sets two.
		{"critical, digitalSignature", x509.KeyUsageDigitalSignature},
		// A pair the default never produces for a leaf.
		{"critical, keyCertSign, cRLSign", x509.KeyUsageCertSign | x509.KeyUsageCRLSign},
		{"critical, dataEncipherment", x509.KeyUsageDataEncipherment},
	} {
		pemText := x509Call(t, r, "x509.create_certificate", value.MapOf(
			"signing_private_key", key, "CN", "x", "keyUsage", tc.usage)).(string)
		cert, err := loadCertificate(pemText)
		if err != nil {
			t.Fatal(err)
		}
		if cert.KeyUsage != tc.want {
			t.Errorf("%q produced key usage %b, want %b", tc.usage, cert.KeyUsage, tc.want)
		}
	}
}

// And the same trap for extendedKeyUsage and the SANs: assert something
// no default supplies.
func TestExtendedKeyUsageAndSANStringAreRead(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := dir + "/k.key"
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algo", "ec"))

	pemText := x509Call(t, r, "x509.create_certificate", value.MapOf(
		"signing_private_key", key, "CN", "x",
		"extendedKeyUsage", "clientAuth, codeSigning",
		"subjectAltName", "DNS:one.example, DNS:two.example, IP:10.0.0.7, email:a@b.example",
	)).(string)
	cert, err := loadCertificate(pemText)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.ExtKeyUsage) != 2 ||
		cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth ||
		cert.ExtKeyUsage[1] != x509.ExtKeyUsageCodeSigning {
		t.Errorf("extended key usage = %v, want [clientAuth codeSigning]", cert.ExtKeyUsage)
	}
	if strings.Join(cert.DNSNames, ",") != "one.example,two.example" {
		t.Errorf("DNS names = %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 1 || cert.IPAddresses[0].String() != "10.0.0.7" {
		t.Errorf("IP addresses = %v", cert.IPAddresses)
	}
	if len(cert.EmailAddresses) != 1 || cert.EmailAddresses[0] != "a@b.example" {
		t.Errorf("email addresses = %v", cert.EmailAddresses)
	}
}

// A certificate gets a new serial and a new expiry every time it is
// written. So a state that re-issued because the group was wrong would
// report a change on every run for as long as it stayed in the tree --
// and this state exists to converge. Ownership is fixed where it stands.
func TestWrongOwnershipIsFixedWithoutReissuing(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing a file's owner needs root")
	}
	if _, err := user.Lookup("nobody"); err != nil {
		t.Skip("this machine has no nobody user to chown to")
	}

	r := New()
	dir := t.TempDir()
	key, crt := dir+"/k.key", dir+"/c.crt"

	if _, err := r.States.Call(newCtx(false), "x509.private_key_managed",
		value.MapOf("name", key, "algo", "ec")); err != nil {
		t.Fatal(err)
	}
	certArgs := func(owner string) *value.Map {
		return value.MapOf("name", crt, "signing_private_key", key, "CN", "x",
			"days_remaining", int64(0), "user", owner)
	}
	if _, err := r.States.Call(newCtx(false), "x509.certificate_managed", certArgs("root")); err != nil {
		t.Fatal(err)
	}

	before, err := loadCertificate(crt)
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.States.Call(newCtx(false), "x509.certificate_managed", certArgs("nobody"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatal("the owner was wrong and nothing changed")
	}
	if _, ok := res.Changes.Get("ownership"); !ok {
		t.Errorf("the change does not mention ownership: %v", res.Changes.StringKeys())
	}
	if !strings.Contains(res.Comment, "ownership") {
		t.Errorf("the comment does not say what changed: %q", res.Comment)
	}

	after, err := loadCertificate(crt)
	if err != nil {
		t.Fatal(err)
	}
	if before.SerialNumber.Cmp(after.SerialNumber) != 0 {
		t.Errorf("the certificate was re-issued to fix an owner: serial %s became %s",
			before.SerialNumber, after.SerialNumber)
	}

	// And now it converges: the same call again is a no-op. That is also
	// what proves the chown happened -- plannedOwnership would still see
	// a difference otherwise, and report one.
	res, err = r.States.Call(newCtx(false), "x509.certificate_managed", certArgs("nobody"))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run still reports a change: %q %v", res.Comment, res.Changes.StringKeys())
	}
}
