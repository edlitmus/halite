package modules

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// certFixture returns paths for a key and a certificate in a fresh
// directory.
func certFixture(t *testing.T) (keyPath, certPath string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "site.key"), filepath.Join(dir, "site.crt")
}

func TestPrivateKeyIsGeneratedThenLeftAlone(t *testing.T) {
	keyPath, _ := certFixture(t)
	args := map[string]any{"name": keyPath}

	r := x509PrivateKeyManaged(&Ctx{}, keyPath, args)
	if !r.Ok || !r.Changed {
		t.Fatalf("want the key generated: %+v", r)
	}
	first := read(t, keyPath)

	if r := x509PrivateKeyManaged(&Ctx{}, keyPath, args); r.Changed {
		t.Fatalf("a second run must not rotate the key: %+v", r)
	}
	if read(t, keyPath) != first {
		t.Fatal("the key on disk changed, which would invalidate every certificate signed from it")
	}
}

func TestPrivateKeyIsPrivate(t *testing.T) {
	keyPath, _ := certFixture(t)
	x509PrivateKeyManaged(&Ctx{}, keyPath, map[string]any{"name": keyPath})

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("a key another account can read is a compromised key; got %04o", perm)
	}
}

func TestALooseKeyModeIsRepairedWithoutRotating(t *testing.T) {
	keyPath, _ := certFixture(t)
	args := map[string]any{"name": keyPath}
	x509PrivateKeyManaged(&Ctx{}, keyPath, args)
	before := read(t, keyPath)

	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	x509PrivateKeyManaged(&Ctx{}, keyPath, args)

	info, _ := os.Stat(keyPath)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("want the mode repaired, got %04o", perm)
	}
	if read(t, keyPath) != before {
		t.Fatal("repairing a mode must not rotate the key")
	}
}

func TestKeyAlgorithms(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"ec by default", map[string]any{}, "P-256 ec"},
		{"rsa at the default size", map[string]any{"algo": "rsa"}, "2048-bit rsa"},
		{"rsa at a named size", map[string]any{"algo": "rsa", "bits": "3072"}, "3072-bit rsa"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keyPath, _ := certFixture(t)
			args := map[string]any{"name": keyPath}
			for k, v := range tc.args {
				args[k] = v
			}
			r := x509PrivateKeyManaged(&Ctx{}, keyPath, args)
			if !r.Changed || !strings.Contains(r.Comment, tc.want) {
				t.Fatalf("want %q, got %+v", tc.want, r)
			}
			// The second run has to recognise what it just wrote.
			if r := x509PrivateKeyManaged(&Ctx{}, keyPath, args); r.Changed {
				t.Fatalf("a key of the wanted kind should be left alone: %+v", r)
			}
		})
	}
}

func TestChangingTheAlgorithmRotatesTheKey(t *testing.T) {
	keyPath, _ := certFixture(t)
	x509PrivateKeyManaged(&Ctx{}, keyPath, map[string]any{"name": keyPath})
	before := read(t, keyPath)

	r := x509PrivateKeyManaged(&Ctx{}, keyPath, map[string]any{"name": keyPath, "algo": "rsa"})
	if !r.Changed || !strings.Contains(r.Comment, "rsa") {
		t.Fatalf("want an rsa key: %+v", r)
	}
	if read(t, keyPath) == before {
		t.Fatal("the key should have been replaced")
	}
}

func TestKeyArgumentsAreChecked(t *testing.T) {
	keyPath, _ := certFixture(t)
	for _, args := range []map[string]any{
		{"name": keyPath, "algo": "dsa"},
		{"name": keyPath, "algo": "rsa", "bits": "1024"},
		{"name": keyPath, "algo": "ec", "bits": "2048"},
	} {
		if r := x509PrivateKeyManaged(&Ctx{}, keyPath, args); r.Ok {
			t.Fatalf("want a failure for %v", args)
		}
	}
}

func TestKeyDryRunWritesNothing(t *testing.T) {
	keyPath, _ := certFixture(t)
	r := x509PrivateKeyManaged(&Ctx{Test: true}, keyPath, map[string]any{"name": keyPath})
	if !r.Ok || !r.Changed {
		t.Fatalf("a dry run should report the pending key: %+v", r)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("a dry run must not write a key")
	}
}

// managedCert generates a key and issues a certificate against it.
func managedCert(t *testing.T, keyPath, certPath string, extra map[string]any) Result {
	t.Helper()
	x509PrivateKeyManaged(&Ctx{}, keyPath, map[string]any{"name": keyPath})
	args := map[string]any{"name": certPath, "private_key": keyPath, "CN": "site.example.com"}
	for k, v := range extra {
		args[k] = v
	}
	return x509CertificateManaged(&Ctx{}, certPath, args)
}

func TestCertificateIsIssuedThenLeftAlone(t *testing.T) {
	keyPath, certPath := certFixture(t)

	if r := managedCert(t, keyPath, certPath, nil); !r.Ok || !r.Changed {
		t.Fatalf("want the certificate issued: %+v", r)
	}
	cert, err := loadCertificate(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "site.example.com" {
		t.Fatalf("unexpected common name %q", cert.Subject.CommonName)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "site.example.com" {
		t.Fatalf("a certificate with no SAN is unusable; got %v", cert.DNSNames)
	}
	if r := managedCert(t, keyPath, certPath, nil); r.Changed {
		t.Fatalf("a current certificate should be left alone: %+v", r)
	}
}

func TestSubjectAlternativeNames(t *testing.T) {
	keyPath, certPath := certFixture(t)
	managedCert(t, keyPath, certPath, map[string]any{
		"subject_alt_names": []any{"DNS:site.example.com", "DNS:www.example.com", "IP:10.0.0.5"},
	})

	cert, err := loadCertificate(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.DNSNames) != 2 || cert.DNSNames[1] != "www.example.com" {
		t.Fatalf("want both names, got %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 1 || cert.IPAddresses[0].String() != "10.0.0.5" {
		t.Fatalf("want the address, got %v", cert.IPAddresses)
	}
}

func TestChangedNamesReissue(t *testing.T) {
	keyPath, certPath := certFixture(t)
	managedCert(t, keyPath, certPath, map[string]any{"subject_alt_names": []any{"DNS:site.example.com"}})

	r := managedCert(t, keyPath, certPath, map[string]any{
		"subject_alt_names": []any{"DNS:site.example.com", "DNS:new.example.com"},
	})
	if !r.Changed || !strings.Contains(r.Comment, "alternative names differ") {
		t.Fatalf("want a reissue for the new name: %+v", r)
	}
}

func TestARotatedKeyReissuesTheCertificate(t *testing.T) {
	keyPath, certPath := certFixture(t)
	managedCert(t, keyPath, certPath, nil)

	x509PrivateKeyManaged(&Ctx{}, keyPath, map[string]any{"name": keyPath, "new": "true"})
	r := x509CertificateManaged(&Ctx{}, certPath, map[string]any{
		"name": certPath, "private_key": keyPath, "CN": "site.example.com",
	})
	if !r.Changed || !strings.Contains(r.Comment, "match its private key") {
		t.Fatalf("a certificate for a key that no longer exists must be reissued: %+v", r)
	}
}

func TestExpiringCertificateIsRenewed(t *testing.T) {
	keyPath, certPath := certFixture(t)
	// A certificate valid for ten days, with a renewal window of thirty:
	// it is inside the window the moment it is issued, which is what a
	// converging fleet relies on to renew itself.
	managedCert(t, keyPath, certPath, map[string]any{"days_valid": "10"})

	r := managedCert(t, keyPath, certPath, map[string]any{"days_valid": "10", "days_remaining": "30"})
	if !r.Changed || !strings.Contains(r.Comment, "renewal window") {
		t.Fatalf("want a renewal: %+v", r)
	}
}

func TestCertificateIsSignedByANamedCA(t *testing.T) {
	dir := t.TempDir()
	caKey := filepath.Join(dir, "ca.key")
	caCert := filepath.Join(dir, "ca.crt")
	x509PrivateKeyManaged(&Ctx{}, caKey, map[string]any{"name": caKey})
	x509CertificateManaged(&Ctx{}, caCert, map[string]any{
		"name": caCert, "private_key": caKey, "CN": "internal ca", "ca": "true",
	})

	keyPath := filepath.Join(dir, "site.key")
	certPath := filepath.Join(dir, "site.crt")
	r := managedCert(t, keyPath, certPath, map[string]any{
		"signing_private_key": caKey, "signing_cert": caCert,
	})
	if !r.Ok || !r.Changed {
		t.Fatalf("want the certificate issued: %+v", r)
	}

	leaf, err := loadCertificate(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Issuer.CommonName != "internal ca" {
		t.Fatalf("want the CA as issuer, got %q", leaf.Issuer.CommonName)
	}
	signer, err := loadCertificate(caCert)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.CheckSignatureFrom(signer); err != nil {
		t.Fatalf("the certificate should verify against its signer: %v", err)
	}
}

func TestCertificateArgumentsAreChecked(t *testing.T) {
	keyPath, certPath := certFixture(t)
	x509PrivateKeyManaged(&Ctx{}, keyPath, map[string]any{"name": keyPath})

	cases := []struct {
		name string
		args map[string]any
	}{
		{"no private key", map[string]any{"name": certPath}},
		{"missing private key", map[string]any{"name": certPath, "private_key": filepath.Join(t.TempDir(), "absent")}},
		{"bad san", map[string]any{"name": certPath, "private_key": keyPath, "subject_alt_names": []any{"IP:not-an-address"}}},
		{"unknown san kind", map[string]any{"name": certPath, "private_key": keyPath, "subject_alt_names": []any{"URI:https://x"}}},
		{"signing key with no cert", map[string]any{"name": certPath, "private_key": keyPath, "signing_private_key": keyPath}},
		{"bad validity", map[string]any{"name": certPath, "private_key": keyPath, "days_valid": "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if r := x509CertificateManaged(&Ctx{}, certPath, tc.args); r.Ok {
				t.Fatalf("want a failure, got %+v", r)
			}
		})
	}
}

func TestCertificateDryRunWritesNothing(t *testing.T) {
	keyPath, certPath := certFixture(t)
	x509PrivateKeyManaged(&Ctx{}, keyPath, map[string]any{"name": keyPath})

	r := x509CertificateManaged(&Ctx{Test: true}, certPath, map[string]any{
		"name": certPath, "private_key": keyPath, "CN": "site.example.com",
	})
	if !r.Ok || !r.Changed {
		t.Fatalf("a dry run should report the pending certificate: %+v", r)
	}
	if _, err := os.Stat(certPath); !os.IsNotExist(err) {
		t.Fatal("a dry run must not write a certificate")
	}
}

func TestIssuedCertificateIsReadableByOtherTools(t *testing.T) {
	keyPath, certPath := certFixture(t)
	managedCert(t, keyPath, certPath, nil)

	// PEM in, DER out, parsed by the standard library the same way openssl
	// would: the point of using crypto/x509 rather than a private format.
	block, rest := pem.Decode([]byte(read(t, certPath)))
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatalf("want one PEM certificate, got %v", block)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(cert.NotAfter) < 300*24*time.Hour {
		t.Fatalf("want a year by default, got %s", cert.NotAfter)
	}
}
