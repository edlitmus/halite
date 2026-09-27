package main

import (
	"context"
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

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/doctor"
	"github.com/edlitmus/halite/internal/pki"
)

// writeCert puts a parseable certificate with an exact validity window in
// a key directory. Self-signed, because `pki.Files.ReadCert` parses and
// does not verify -- what this check reads is NotBefore and NotAfter, and
// building a real chain would test the CA rather than the check.
func writeCert(t *testing.T, dir, file, cn string, notBefore, notAfter time.Time) {
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
	body := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, file), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// hubDoctorCerts runs the hub's certificate check against a key directory
// written here, which is the only way to tell what it actually looks at.
func hubDoctorCerts(t *testing.T, pkiDir string) doctor.Result {
	t.Helper()
	cfg, err := config.Load(config.Hub, config.LoadOptions{Root: t.TempDir(), AllowMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	args := &cli.Args{Flags: map[string]string{"pki-dir": pkiDir}}
	return hubCertificateCheck(args, cfg).Run(context.Background())
}

// TestTheCertificateCheckCoversOperatorCertificates. The check read
// exactly two files, `hub.crt` and `ca.crt`, and was named "certificate
// validity and expiry" -- so on an estate whose operator credential had
// expired it reported
//
//	pass  certificate validity and expiry  the enrollment CA: 3619 days
//	      left; this hub's certificate: 59 days left
//
// while `halite-hub run '*' test.ping` failed the handshake on the
// expired operator certificate. Measured: the pass and the failure were
// observed in the same minute on the same hub. The check was truthful
// about what it looked at and silent about the thing that was broken.
// DIVERGENCE 5.159.
func TestTheCertificateCheckCoversOperatorCertificates(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	// A healthy hub pair, so the only unhealthy thing is the operator's.
	writeCert(t, dir, pki.HubCertFile, "halite hub", now.Add(-time.Hour), now.Add(59*24*time.Hour))
	writeCert(t, dir, pki.CACertFile, "halite enrollment CA", now.Add(-time.Hour), now.Add(3600*24*time.Hour))
	writeCert(t, dir, pki.OperatorCertFile("ed"), "ed", now.Add(-31*24*time.Hour), now.Add(-6*time.Hour))

	res := hubDoctorCerts(t, dir)
	if res.Status != doctor.Fail {
		t.Errorf("an expired operator certificate is %s, not fail: %s", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "operator ed") {
		t.Errorf("the check does not mention the operator certificate at all: %s", res.Detail)
	}
	if !strings.Contains(res.Detail, "expired") {
		t.Errorf("the expiry is not reported: %s", res.Detail)
	}
	// The remedy has to name the command that fixes *this* certificate.
	// `halite-node renew` does not reissue an operator's.
	if !strings.Contains(res.Remedy, "keys operator create") {
		t.Errorf("the remedy does not say how to reissue an operator certificate: %s", res.Remedy)
	}
}

// TestAFreshOperatorCertificateDoesNotWarn. `keys operator create`
// defaults to a 720h lifetime, and the hub's own window is thirty days.
// Checking operator certificates against that window would report every
// default-lifetime credential as expiring on the day it was issued --
// permanently yellow, and therefore read by nobody. The operator window
// is a week for that reason, and this is the assertion that keeps the two
// numbers from being set independently. DIVERGENCE 5.159.
func TestAFreshOperatorCertificateDoesNotWarn(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	writeCert(t, dir, pki.HubCertFile, "halite hub", now.Add(-time.Hour), now.Add(59*24*time.Hour))
	writeCert(t, dir, pki.CACertFile, "halite enrollment CA", now.Add(-time.Hour), now.Add(3600*24*time.Hour))
	// Issued a minute ago with the tool's own default lifetime.
	writeCert(t, dir, pki.OperatorCertFile("fresh"), "fresh",
		now.Add(-time.Minute), now.Add(720*time.Hour-time.Minute))

	res := hubDoctorCerts(t, dir)
	if res.Status != doctor.Pass {
		t.Errorf("a freshly issued default-lifetime operator certificate is %s: %s",
			res.Status, res.Detail)
	}

	// And one genuinely close to lapsing still warns, or the window has
	// simply been widened until it never fires.
	writeCert(t, dir, pki.OperatorCertFile("soon"), "soon",
		now.Add(-29*24*time.Hour), now.Add(36*time.Hour))
	if res := hubDoctorCerts(t, dir); res.Status != doctor.Warn {
		t.Errorf("an operator certificate 36h from expiry is %s, not warn: %s",
			res.Status, res.Detail)
	}
}

// TestAnAbsentCertificateIsReportedAndAnEmptyDirectoryIsNot. Two states
// that a single silent `continue` had made indistinguishable.
//
// An empty key directory is a hub nobody has set up yet, and saying
// "absent" twice about it would be noise. A directory holding a CA and no
// certificate of its own is a broken hub, and it used to pass on the half
// it still had. With operator certificates in scope the distinction
// stops being academic: they come and go, so "the file is not there" and
// "the file is there and expired" must not collapse into the same
// answer. DIVERGENCE 5.159.
func TestAnAbsentCertificateIsReportedAndAnEmptyDirectoryIsNot(t *testing.T) {
	if res := hubDoctorCerts(t, t.TempDir()); res.Status != doctor.Skip {
		t.Errorf("an empty key directory is %s, not skip: %s", res.Status, res.Detail)
	}

	now := time.Now()
	dir := t.TempDir()
	writeCert(t, dir, pki.CACertFile, "halite enrollment CA", now.Add(-time.Hour), now.Add(3600*24*time.Hour))

	res := hubDoctorCerts(t, dir)
	if res.Status != doctor.Fail {
		t.Errorf("a hub with a CA and no certificate of its own is %s: %s", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "not present") {
		t.Errorf("the absent certificate is not named as absent: %s", res.Detail)
	}
}
