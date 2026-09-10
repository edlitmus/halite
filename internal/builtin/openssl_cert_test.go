package builtin

import (
	"os"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The fixtures here are output captured from a real openssl, not written
// from memory: OpenSSL 3.5.6 on the FreeBSD host this project is
// developed on, run against a CA and a leaf made for the purpose. The
// 1.1.1 spellings are the ones that differ, and they are here because
// half of any real estate is still on them.

const opensslVerifyFailure = `CN=leaf.example
error 20 at 0 depth lookup: unable to get local issuer certificate
error leaf.crt: verification failed
`

// OpenSSL 1.1.1 writes the subject on the same line as the file and
// leaves no space after `lookup:`. Both differences are in this one
// fixture because both are in this one line.
const opensslVerifyFailure111 = `leaf.crt: CN = leaf.example
error 20 at 0 depth lookup:unable to get local issuer certificate
`

const opensslVerifyOK = `leaf.crt: OK
Chain:
depth=0: CN=leaf.example (untrusted)
depth=1: CN=Test CA
`

const opensslExpired = `CN=old.example
error 10 at 0 depth lookup: certificate has expired
error old.crt: verification failed
`

const opensslCRLText = `Certificate Revocation List (CRL):
        Version 2 (0x1)
        Signature Algorithm: sha256WithRSAEncryption
        Issuer: CN=Test CA
        Last Update: Sep 10 16:18:46 2026 GMT
        Next Update: Oct 10 16:18:46 2026 GMT
        CRL extensions:
            X509v3 CRL Number:
                2
Revoked Certificates:
    Serial Number: 455778517115B5A71510C30B88F10477A0E77160
        Revocation Date: Sep 10 16:18:46 2026 GMT
    Serial Number: 0A1B2C3D
        Revocation Date: Aug  1 09:00:00 2026 GMT
    Signature Algorithm: sha256WithRSAEncryption
`

// The numbered reason is read on both spellings openssl has.
//
// The number is what decides who gets woken up: 10 is a certificate that
// expired, which is somebody's Tuesday, and 20 is a missing issuer,
// which is usually a deployment that shipped without its chain. A reader
// that handled only OpenSSL 3's spacing would return an empty reason on
// every 1.1.1 node while still reporting the failure — right about the
// failure, silent about the cause, and noticed late.
func TestTheVerifyFailureIsReadOnBothOpenSSLSpellings(t *testing.T) {
	for name, out := range map[string]string{
		"openssl 3":     opensslVerifyFailure,
		"openssl 1.1.1": opensslVerifyFailure111,
	} {
		code, reason := openSSLVerifyFailure(out)
		if code != 20 {
			t.Errorf("%s: read the error code as %d, want 20", name, code)
		}
		if reason != "unable to get local issuer certificate" {
			t.Errorf("%s: read the reason as %q", name, reason)
		}
	}

	code, reason := openSSLVerifyFailure(opensslExpired)
	if code != 10 || reason != "certificate has expired" {
		t.Errorf("an expired certificate read as %d %q, want 10 and the expiry reason", code, reason)
	}

	// A verification that succeeded has no numbered reason, and the
	// summary line `error leaf.crt: verification failed` is not one — it
	// carries a filename where the code belongs.
	if code, reason := openSSLVerifyFailure(opensslVerifyOK); code != 0 || reason != "" {
		t.Errorf("a successful verification read as error %d %q", code, reason)
	}
	if code, _ := openSSLVerifyFailure("error leaf.crt: verification failed\n"); code != 0 {
		t.Errorf("the summary line was read as error code %d", code)
	}
}

// The chain listing separates what was trusted from what travelled with
// the certificate.
//
// `(untrusted)` means openssl found that certificate in the file being
// verified rather than in the trust store. The difference matters: a
// chain where the CA is marked untrusted verified because the
// certificate brought its own issuer, and installing the CA is still
// outstanding.
func TestTheChainListingKeepsTrustedAndUntrustedApart(t *testing.T) {
	chain := openSSLVerifyChain(opensslVerifyOK)
	if len(chain) != 2 {
		t.Fatalf("read %d chain entries, want 2", len(chain))
	}
	leaf := chain[0].(*value.Map)
	ca := chain[1].(*value.Map)

	if depth, _ := leaf.GetString("depth"); depth != int64(0) {
		t.Errorf("the leaf is at depth %v, want 0", depth)
	}
	if subject, _ := leaf.GetString("subject"); subject != "CN=leaf.example" {
		t.Errorf("the leaf's subject reads as %q; the marker should not be part of the name", subject)
	}
	if trusted, _ := leaf.GetString("trusted"); trusted != false {
		t.Error("the leaf came from the file being verified and is reported as trusted")
	}
	if subject, _ := ca.GetString("subject"); subject != "CN=Test CA" {
		t.Errorf("the CA's subject reads as %q", subject)
	}
	if trusted, _ := ca.GetString("trusted"); trusted != true {
		t.Error("the CA came from the trust store and is reported as untrusted")
	}

	// LibreSSL prints no chain at all, and no chain is not an error.
	if got := openSSLVerifyChain("leaf.crt: OK\n"); len(got) != 0 {
		t.Errorf("a report with no chain produced %d entries", len(got))
	}
}

// Every revoked serial is read with the date beside it.
//
// The two lines are labelled and adjacent, so the reader keys on the
// labels. A reader that counted lines would attach the trailing
// `Signature Algorithm:` block to the last serial, and one that split on
// whitespace would take `Aug  1 09:00:00 2026 GMT` — which has two
// spaces in it, because openssl pads the day — as several fields.
func TestEveryRevokedSerialIsReadWithItsDate(t *testing.T) {
	revoked := openSSLRevoked(opensslCRLText)
	if len(revoked) != 2 {
		t.Fatalf("read %d revoked certificates, want 2", len(revoked))
	}
	first := revoked[0].(*value.Map)
	second := revoked[1].(*value.Map)

	if serial, _ := first.GetString("serial"); serial != "455778517115B5A71510C30B88F10477A0E77160" {
		t.Errorf("the first serial reads as %q", serial)
	}
	if date, _ := first.GetString("revoked"); date != "Sep 10 16:18:46 2026 GMT" {
		t.Errorf("the first revocation date reads as %q", date)
	}
	// The padded day is the one a whitespace split gets wrong.
	if date, _ := second.GetString("revoked"); date != "Aug  1 09:00:00 2026 GMT" {
		t.Errorf("the second revocation date reads as %q, want the padded day kept", date)
	}

	// A CA with nothing revoked is the healthy case, not a failure.
	empty := openSSLRevoked("Certificate Revocation List (CRL):\nNo Revoked Certificates.\n")
	if len(empty) != 0 {
		t.Errorf("an empty list produced %d entries", len(empty))
	}
}

// A passphrase never reaches the argument vector.
//
// Every account on the node can read another's command line, through
// `ps`, /proc/<pid>/cmdline or `procstat -c`. So `-passin pass:secret`
// publishes the passphrase for as long as the process runs, and
// `-passin env:VAR` does the same through /proc on Linux. This is the
// assertion that neither is ever built.
func TestAPassphraseNeverReachesTheArgumentVector(t *testing.T) {
	for _, role := range []string{"-passin", "-passout"} {
		argv := openSSLPasswordArgv(role)
		if len(argv) != 2 || argv[0] != role || argv[1] != "stdin" {
			t.Fatalf("%s is passed as %v, want the passphrase read from standard input", role, argv)
		}
	}
}

// The whole module is swept for the three ways a secret could reach the
// command line.
//
// The test above pins the helper; this one pins that nothing bypasses
// it. It reads the source of the module rather than calling it, which is
// the only way to assert about a spelling that is not there.
func TestNoOpenSSLCallBuildsAPasswordArgument(t *testing.T) {
	source := readModuleSource(t, "openssl_cert.go")
	for _, forbidden := range []string{`"pass:`, `"env:`, `"file:`, `"fd:`} {
		if strings.Contains(source, forbidden) {
			t.Errorf("openssl_cert.go builds a %s password argument, which puts the passphrase where `ps` can read it", forbidden)
		}
	}
}

// `-show_chain` is passed to OpenSSL and withheld from LibreSSL.
//
// LibreSSL is not OpenSSL with a different banner: it has no
// `-show_chain`, and passing it there makes every verification fail with
// a usage error — on OpenBSD, which is the platform nobody here runs.
// The flavour is read from the banner rather than from the platform,
// because FreeBSD has OpenSSL in base and LibreSSL in ports and a node
// can have either.
func TestShowChainIsWithheldFromLibreSSL(t *testing.T) {
	openssl := openSSLInfo{Path: "/usr/bin/openssl", Banner: "OpenSSL 3.5.6 7 Apr 2026"}
	libre := openSSLInfo{Path: "/usr/bin/openssl", Banner: "LibreSSL 3.9.2", LibreSSL: true}

	got := strings.Join(openSSLVerifyArgv(openssl, "leaf.crt", "ca.crt", "", "", "", "", ""), " ")
	if !strings.Contains(got, "-show_chain") {
		t.Errorf("OpenSSL was not asked for the chain: %s", got)
	}
	got = strings.Join(openSSLVerifyArgv(libre, "leaf.crt", "ca.crt", "", "", "", "", ""), " ")
	if strings.Contains(got, "-show_chain") {
		t.Errorf("LibreSSL was passed an option it does not have: %s", got)
	}
	// And the certificate is last on both, because openssl takes it
	// positionally and an option after it is read as another certificate.
	for _, argv := range [][]string{
		openSSLVerifyArgv(openssl, "leaf.crt", "ca.crt", "", "", "", "sslserver", "leaf.example"),
		openSSLVerifyArgv(libre, "leaf.crt", "", "/etc/ssl/certs", "chain.pem", "", "", ""),
	} {
		if argv[len(argv)-1] != "leaf.crt" {
			t.Errorf("the certificate is not the last argument: %v", argv)
		}
	}
}

// Checking revocation checks the whole chain.
//
// `-crl_check` alone checks the leaf, which leaves a revoked
// intermediate trusted — and an intermediate is what gets revoked when a
// CA is compromised, which is the case the check exists for.
func TestRevocationCheckingCoversTheWholeChain(t *testing.T) {
	openssl := openSSLInfo{Path: "openssl", Banner: "OpenSSL 3.5.6"}
	got := strings.Join(openSSLVerifyArgv(openssl, "leaf.crt", "ca.crt", "", "", "crl.pem", "", ""), " ")
	if !strings.Contains(got, "-crl_check_all") {
		t.Errorf("revocation is checked with %q, which does not cover the intermediates", got)
	}
	// And nothing asks for revocation when no list was given, because
	// `-crl_check_all` with no CRL fails every verification.
	got = strings.Join(openSSLVerifyArgv(openssl, "leaf.crt", "ca.crt", "", "", "", "", ""), " ")
	if strings.Contains(got, "crl") {
		t.Errorf("a verification with no revocation list still asked for one: %s", got)
	}
}

// readModuleSource returns one file of this package, for the few
// assertions that are about a spelling that must not appear anywhere
// rather than about what a function returns.
func readModuleSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s could not be read: %v", name, err)
	}
	return string(b)
}

// A verdict is told apart from a failure to read the files.
//
// This is the discriminator that decides whether `verify` answers or
// refuses, and getting it wrong in either direction is a wrong thing to
// tell an operator: reporting a load failure as an untrusted certificate
// sends them to renew one that is fine, and reporting an untrusted
// certificate as a load failure hides the answer they asked for.
//
// It is keyed on the presence of a verdict rather than on the wording of
// a load error, because the load error is a different message per
// flavour and per version. The last case is the real one this host's
// openssl produced against an empty -CAfile.
func TestAVerdictIsToldApartFromAFailureToReadTheFiles(t *testing.T) {
	for name, c := range map[string]struct {
		stdout, stderr string
		examined       bool
	}{
		"a certificate that passed":        {opensslVerifyOK, "", true},
		"a certificate that failed":        {opensslVerifyFailure, "", true},
		"the 1.1.1 wording":                {opensslVerifyFailure111, "", true},
		"an expired certificate":           {opensslExpired, "", true},
		"a verdict that arrived on stderr": {"", opensslVerifyFailure, true},
		"an empty trust file": {"", "Error loading file empty.pem\n" +
			"10B005B776580000:error:05800087:x509 certificate routines:" +
			"X509_load_cert_file_ex:no certificate found:by_file.c:153:", false},
		"nothing at all": {"", "", false},
	} {
		if got := openSSLExamined(c.stdout, c.stderr); got != c.examined {
			t.Errorf("%s: openssl was read as having examined=%v, want %v", name, got, c.examined)
		}
	}
}
