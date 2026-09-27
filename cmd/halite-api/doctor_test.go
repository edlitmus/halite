package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// writeAPICert puts a parseable certificate with an exact validity window
// on disk. Self-signed, because this check parses and does not verify.
func writeAPICert(t *testing.T, path, cn string, notBefore, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDoctorCoversTheThreeCertificatesThisServiceHolds. The API had no
// `doctor`, so nothing checked any of the three certificates it depends
// on: the one it presents to callers, the operator certificate it
// presents to the hub, and the CA it verifies the hub with. The hub's own
// `doctor` could not close the gap -- they are named in `api.yaml`, which
// is another service's configuration. DIVERGENCE 5.160.
func TestDoctorCoversTheThreeCertificatesThisServiceHolds(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	if err := os.MkdirAll(pkiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serving := filepath.Join(root, "api.crt")
	writeAPICert(t, serving, "api.example", now.Add(-time.Hour), now.Add(700*24*time.Hour))
	writeAPICert(t, filepath.Join(pkiDir, pki.CACertFile), "halite enrollment CA",
		now.Add(-time.Hour), now.Add(3600*24*time.Hour))
	operatorCert := filepath.Join(pkiDir, pki.OperatorCertFile("api"))
	writeAPICert(t, operatorCert, "api", now.Add(-time.Hour), now.Add(80*24*time.Hour))
	if err := os.WriteFile(filepath.Join(root, "api.yaml"),
		[]byte("tls_cert: "+serving+"\nhub: 127.0.0.1:4599\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// All three healthy first, because a passing check lists everything it
	// looked at -- which is the only place the *coverage* is visible. A
	// failing one lists only what is wrong.
	out, errb, _ := run(t, "doctor", "--root", root, "--pki-dir", pkiDir)
	all := out + errb
	for _, want := range []string{
		"the certificate this service presents",
		"the operator certificate for api",
		"the hub's CA",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("doctor does not cover %q:\n%s", want, all)
		}
	}

	// Now the failure this whole exercise started from: the operator
	// certificate expired six hours ago and the service still looks fine
	// from the outside.
	writeAPICert(t, operatorCert, "api", now.Add(-31*24*time.Hour), now.Add(-6*time.Hour))
	out, errb, code := run(t, "doctor", "--root", root, "--pki-dir", pkiDir)
	all = out + errb
	if code == 0 {
		t.Errorf("an expired operator certificate exited 0:\n%s", all)
	}
	if !strings.Contains(all, "the operator certificate for api expired") {
		t.Errorf("the expiry is not reported:\n%s", all)
	}
	// The remedy has to name the command that reissues an operator's.
	if !strings.Contains(all, "keys operator create") {
		t.Errorf("no remedy for an operator certificate:\n%s", all)
	}
}

// TestDoctorReportsEveryCheckRatherThanExitingOnTheFirst. `hubClient`
// called `cli.Fatalf`, which is right for `serve` and wrong here: with no
// operator certificate on disk the connectivity probe killed the whole
// run, so an operator asking what was wrong was told one thing and left
// guessing about the rest. DIVERGENCE 5.160.
func TestDoctorReportsEveryCheckRatherThanExitingOnTheFirst(t *testing.T) {
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	if err := os.MkdirAll(pkiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No operator certificate and no CA at all, and a hub that is not
	// listening: the state where exiting early hides the most.
	if err := os.WriteFile(filepath.Join(root, "api.yaml"),
		[]byte("hub: 127.0.0.1:4599\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errb, code := run(t, "doctor", "--root", root, "--pki-dir", pkiDir)
	all := out + errb
	if code == 0 {
		t.Errorf("a service with no key material exited 0:\n%s", all)
	}
	// Every check ran, including the ones after the one that failed.
	for _, want := range []string{
		"configuration validity",
		"certificate validity and expiry",
		"connectivity",
		"FIPS mode consistency",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("doctor stopped before %q:\n%s", want, all)
		}
	}
	// And it says which certificate is missing rather than only that
	// something is.
	if !strings.Contains(all, "cannot authenticate to the hub") {
		t.Errorf("the absent operator certificate is not named:\n%s", all)
	}
	// The connectivity remedy must not send an API operator to the
	// node's command.
	if strings.Contains(all, "`halite-node doctor` checks the certificate") {
		t.Errorf("the connectivity remedy names the wrong command:\n%s", all)
	}
}

// TestDoctorRendersJSONForAState. SPEC 26.4's argument for one command is
// that a state can read it, so the structured form has to carry the same
// answer as the text.
func TestDoctorRendersJSONForAState(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "api.yaml"), []byte("hub: 127.0.0.1:4599\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, _ := run(t, "doctor", "--root", root, "--out", "json")
	for _, want := range []string{`"role":"api"`, `"checks"`, `"worst"`, `"counts"`} {
		if !strings.Contains(out, want) {
			t.Errorf("--out json has no %s:\n%s", want, out)
		}
	}
}
