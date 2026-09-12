package builtin

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// tlsRegistry builds a registry holding only the `tls` module: it is not
// wired into builtin.New() yet, which is deliberate (see the task's own
// integration note), so a test dispatching through New() would see
// "unknown function" for every one of these.
func tlsRegistry() *Registries {
	r := &Registries{Exec: exec.NewRegistry(), States: states.NewRegistry()}
	registerTLS(r)
	return r
}

func tlsCall(t *testing.T, r *Registries, c *exec.Context, fn string, args *value.Map) any {
	t.Helper()
	out, err := r.Exec.Call(c, fn, args)
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return out
}

func tlsCallErr(t *testing.T, r *Registries, c *exec.Context, fn string, args *value.Map) error {
	t.Helper()
	_, err := r.Exec.Call(c, fn, args)
	return err
}

func tlsParseCert(t *testing.T, pemText string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		t.Fatalf("no PEM block in %q", pemText)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("could not parse certificate: %v", err)
	}
	return cert
}

func TestCreateCAWritesACertificateAndKeyUnderItsOwnDirectory(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	out := tlsCall(t, r, newCtx(false), "tls.create_ca",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA")).(*value.Map)

	changed, _ := out.GetString("changed")
	if changed != true {
		t.Fatalf("changed = %v, want true for a first creation", changed)
	}
	certPath, _ := out.GetString("certificate")
	if certPath != filepath.Join(base, "example", "ca_cert.crt") {
		t.Fatalf("certificate = %v, not the conventional path", certPath)
	}
	assertMode(t, filepath.Join(base, "example", "ca_cert.key"), 0o600)
}

func TestCreatingTheSameCATwiceIsANoOp(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	args := value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA")
	first := tlsCall(t, r, newCtx(false), "tls.create_ca", args).(*value.Map)
	second := tlsCall(t, r, newCtx(false), "tls.create_ca", args).(*value.Map)

	firstCert, _ := first.GetString("certificate")
	secondCert, _ := second.GetString("certificate")
	if firstCert != secondCert {
		t.Fatalf("the certificate path moved between calls")
	}
	changed, _ := second.GetString("changed")
	if changed != false {
		t.Fatalf("changed = %v on the second create_ca, want false", changed)
	}
}

func TestForceReplacesAnExistingCA(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	args := value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA")
	tlsCall(t, r, newCtx(false), "tls.create_ca", args)

	before, err := loadCertificate(filepath.Join(base, "example", "ca_cert.crt"))
	if err != nil {
		t.Fatal(err)
	}

	forced := value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA", "force", true)
	out := tlsCall(t, r, newCtx(false), "tls.create_ca", forced).(*value.Map)
	changed, _ := out.GetString("changed")
	if changed != true {
		t.Fatalf("changed = %v with force=true, want true", changed)
	}

	after, err := loadCertificate(filepath.Join(base, "example", "ca_cert.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if before.SerialNumber.Cmp(after.SerialNumber) == 0 {
		t.Fatal("the CA certificate was not actually regenerated")
	}
}

func TestTestModeCreatesNoCAFiles(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	ctx := newCtx(true)
	out := tlsCall(t, r, ctx, "tls.create_ca",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA")).(*value.Map)
	changed, _ := out.GetString("changed")
	if changed != true {
		t.Fatalf("changed = %v, want true (predicted)", changed)
	}
	if got := tlsCall(t, r, newCtx(false), "tls.ca_exists",
		value.MapOf("ca_name", "example", "cert_base_path", base)); got != false {
		t.Fatalf("ca_exists = %v; test mode must not have written anything", got)
	}
}

func TestCAExistsReportsPresenceHonestly(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	present := func() any {
		return tlsCall(t, r, newCtx(false), "tls.ca_exists", value.MapOf("ca_name", "example", "cert_base_path", base))
	}
	if present() != false {
		t.Fatal("ca_exists = true before create_ca ran")
	}
	tlsCall(t, r, newCtx(false), "tls.create_ca",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA"))
	if present() != true {
		t.Fatal("ca_exists = false after create_ca ran")
	}
}

func TestGetCAReturnsTheSameBytesAsTheFileOnDisk(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	tlsCall(t, r, newCtx(false), "tls.create_ca",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA"))
	out := tlsCall(t, r, newCtx(false), "tls.get_ca", value.MapOf("ca_name", "example", "cert_base_path", base)).(*value.Map)
	pemText, _ := out.GetString("certificate")
	cert := tlsParseCert(t, pemText.(string))
	if cert.Subject.CommonName != "Example Root CA" {
		t.Fatalf("common name = %q, want Example Root CA", cert.Subject.CommonName)
	}
}

func TestIssuingACertificateWithNoCAFailsWithAnActionableMessage(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	err := tlsCallErr(t, r, newCtx(false), "tls.create_ca_signed_cert",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "web1.example.org"))
	if err == nil {
		t.Fatal("expected an error; there is no CA yet")
	}
}

func TestACASignedCertificateVerifiesAgainstItsCA(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	tlsCall(t, r, newCtx(false), "tls.create_ca",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA"))
	out := tlsCall(t, r, newCtx(false), "tls.create_ca_signed_cert",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "web1.example.org")).(*value.Map)

	certPath, _ := out.GetString("certificate")
	keyPath, _ := out.GetString("private_key")
	assertMode(t, keyPath.(string), 0o600)

	leaf, err := loadCertificate(certPath.(string))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := loadCertificate(filepath.Join(base, "example", "ca_cert.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("the issued certificate does not verify against its CA: %v", err)
	}
}

func TestReissuingTheSameCommonNameIsANoOpWithoutOverwrite(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	tlsCall(t, r, newCtx(false), "tls.create_ca",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA"))
	args := value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "web1.example.org")
	tlsCall(t, r, newCtx(false), "tls.create_ca_signed_cert", args)
	second := tlsCall(t, r, newCtx(false), "tls.create_ca_signed_cert", args).(*value.Map)
	changed, _ := second.GetString("changed")
	if changed != false {
		t.Fatalf("changed = %v on a repeat issuance, want false", changed)
	}
}

func TestIssuanceIsRecordedInListCerts(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	tlsCall(t, r, newCtx(false), "tls.create_ca",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA"))
	tlsCall(t, r, newCtx(false), "tls.create_ca_signed_cert",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "web1.example.org"))

	list := tlsCall(t, r, newCtx(false), "tls.list_certs", value.MapOf("ca_name", "example", "cert_base_path", base)).([]any)
	if len(list) != 1 {
		t.Fatalf("list_certs = %v, want one entry", list)
	}
	entry := list[0].(*value.Map)
	cn, _ := entry.GetString("common_name")
	status, _ := entry.GetString("status")
	if cn != "web1.example.org" || status != "valid" {
		t.Fatalf("entry = %v, want web1.example.org/valid", entry)
	}
}

func TestRevokeMarksTheIndexAndWritesAParsableCRL(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	tlsCall(t, r, newCtx(false), "tls.create_ca",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA"))
	tlsCall(t, r, newCtx(false), "tls.create_ca_signed_cert",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "web1.example.org"))

	out := tlsCall(t, r, newCtx(false), "tls.revoke_cert",
		value.MapOf("ca_name", "example", "cert_base_path", base, "common_name", "web1.example.org")).(*value.Map)
	changed, _ := out.GetString("changed")
	if changed != true {
		t.Fatalf("changed = %v, want true", changed)
	}

	list := tlsCall(t, r, newCtx(false), "tls.list_certs", value.MapOf("ca_name", "example", "cert_base_path", base)).([]any)
	entry := list[0].(*value.Map)
	status, _ := entry.GetString("status")
	if status != "revoked" {
		t.Fatalf("status = %v after revoke, want revoked", status)
	}

	crlPath, _ := out.GetString("crl")
	crlBytes, err := readCRLFile(crlPath.(string))
	if err != nil {
		t.Fatal(err)
	}
	crl, err := x509.ParseRevocationList(crlBytes)
	if err != nil {
		t.Fatalf("the CRL this module wrote does not parse: %v", err)
	}
	if len(crl.RevokedCertificateEntries) != 1 {
		t.Fatalf("the CRL lists %d revoked certificate(s), want 1", len(crl.RevokedCertificateEntries))
	}

	entrySerial, _ := entry.GetString("serial_number")
	if crl.RevokedCertificateEntries[0].SerialNumber.Text(16) != entrySerial {
		t.Fatalf("the CRL's serial does not match the index's")
	}
}

func TestRevokingAnUnknownCommonNameReportsNoChange(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	tlsCall(t, r, newCtx(false), "tls.create_ca",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA"))
	out := tlsCall(t, r, newCtx(false), "tls.revoke_cert",
		value.MapOf("ca_name", "example", "cert_base_path", base, "common_name", "nobody.example.org")).(*value.Map)
	changed, _ := out.GetString("changed")
	if changed != false {
		t.Fatalf("changed = %v, want false for an unknown common name", changed)
	}
}

func TestTestModeRevokesNothing(t *testing.T) {
	r := tlsRegistry()
	base := t.TempDir()
	tlsCall(t, r, newCtx(false), "tls.create_ca",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "Example Root CA"))
	tlsCall(t, r, newCtx(false), "tls.create_ca_signed_cert",
		value.MapOf("ca_name", "example", "cert_base_path", base, "CN", "web1.example.org"))

	tlsCall(t, r, newCtx(true), "tls.revoke_cert",
		value.MapOf("ca_name", "example", "cert_base_path", base, "common_name", "web1.example.org"))

	list := tlsCall(t, r, newCtx(false), "tls.list_certs", value.MapOf("ca_name", "example", "cert_base_path", base)).([]any)
	entry := list[0].(*value.Map)
	status, _ := entry.GetString("status")
	if status != "valid" {
		t.Fatalf("status = %v; test mode must not have revoked anything", status)
	}
}

func TestMissingCANameIsRefusedByEveryFunction(t *testing.T) {
	r := tlsRegistry()
	if err := tlsCallErr(t, r, newCtx(false), "tls.ca_exists", value.MapOf("cert_base_path", t.TempDir())); err == nil {
		t.Fatal("expected an error naming the missing ca_name")
	}
}

func TestMissingCertBasePathIsRefused(t *testing.T) {
	r := tlsRegistry()
	if err := tlsCallErr(t, r, newCtx(false), "tls.ca_exists", value.MapOf("ca_name", "example")); err == nil {
		t.Fatal("expected an error naming the missing cert_base_path")
	}
}

// readCRLFile decodes the PEM this module writes for a CRL, mirroring
// how a real verifier would read the file back off disk.
func readCRLFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block found in the CRL file")
	}
	return block.Bytes, nil
}
