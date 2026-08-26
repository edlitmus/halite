package builtin

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/fips"
	"github.com/edlitmus/halite/internal/value"
)

func x509Call(t *testing.T, r *Registries, fn string, args *value.Map) any {
	t.Helper()
	out, err := r.Exec.Call(newCtx(false), fn, args)
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return out
}

// SPEC 15.2 sets a floor rather than a default: a tree asking for a weak
// key is told it is wrong, not quietly given a strong one, because the
// tree will otherwise keep asking.
func TestWeakKeysAreRefused(t *testing.T) {
	r := New()
	for _, args := range []*value.Map{
		value.MapOf("algorithm", "rsa", "bits", int64(1024)),
		value.MapOf("algorithm", "rsa", "bits", int64(2047)),
		value.MapOf("algorithm", "ec", "curve", "p224"),
		value.MapOf("algorithm", "dsa"),
	} {
		if _, err := r.Exec.Call(newCtx(false), "x509.create_private_key", args); err == nil {
			t.Errorf("%v was accepted; it should be refused", args.StringKeys())
		}
	}

	// And the floor is a floor, not a ceiling.
	if _, err := r.Exec.Call(newCtx(false), "x509.create_private_key",
		value.MapOf("algorithm", "rsa", "bits", int64(2048))); err != nil {
		t.Errorf("2048 bits is the minimum and should be accepted: %v", err)
	}
}

func TestPrivateKeyIsWrittenUnreadableToOthers(t *testing.T) {
	r := New()
	path := filepath.Join(t.TempDir(), "key.pem")
	x509Call(t, r, "x509.create_private_key",
		value.MapOf("path", path, "algorithm", "ec", "curve", "p256"))

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %04o, want 0600; a private key must not be readable by anyone else", perm)
	}

	key, err := loadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := key.(*ecdsa.PrivateKey); !ok {
		t.Errorf("key = %T, want an ECDSA key", key)
	}
}

func TestEveryAlgorithmRoundTrips(t *testing.T) {
	r := New()
	for _, args := range []*value.Map{
		value.MapOf("algorithm", "rsa", "bits", int64(2048)),
		value.MapOf("algorithm", "ec", "curve", "p256"),
		value.MapOf("algorithm", "ec", "curve", "p384"),
		value.MapOf("algorithm", "ed25519"),
	} {
		algorithm, _ := args.Get("algorithm")
		if algorithm == "ed25519" && fips.Restricted() {
			// SPEC 27.4: not approved, and refused by name rather than
			// left to fail inside the module. The refusal is the
			// assertion, and it has to name the setting to change.
			_, err := r.Exec.Call(newCtx(false), "x509.create_private_key", args)
			if err == nil {
				t.Error("ed25519 was generated in FIPS mode")
			} else if !strings.Contains(err.Error(), "not approved under FIPS 140-3") {
				t.Errorf("ed25519 was refused for the wrong reason: %v", err)
			}
			continue
		}
		out := x509Call(t, r, "x509.create_private_key", args)
		pemText, ok := out.(string)
		if !ok || !strings.Contains(pemText, "BEGIN PRIVATE KEY") {
			t.Fatalf("%v -> %#v", args.StringKeys(), out)
		}
		if _, err := loadPrivateKey(pemText); err != nil {
			t.Errorf("%v: the key it wrote does not load back: %v", args.StringKeys(), err)
		}
	}
}

// A whole small PKI, because the parts only matter together: a CA, a leaf
// signed by it, and the checks a tree makes on the result.
func TestCASignedCertificate(t *testing.T) {
	r := New()
	dir := t.TempDir()

	caKey := filepath.Join(dir, "ca.key")
	caCert := filepath.Join(dir, "ca.pem")
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", caKey, "algorithm", "ec"))
	x509Call(t, r, "x509.create_certificate", value.MapOf(
		"path", caCert, "private_key", caKey, "CN", "Test CA", "O", "halite",
		"ca", true, "days_valid", int64(3650)))

	leafKey := filepath.Join(dir, "leaf.key")
	leafCert := filepath.Join(dir, "leaf.pem")
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", leafKey, "algorithm", "ec"))
	x509Call(t, r, "x509.create_certificate", value.MapOf(
		"path", leafCert, "private_key", leafKey,
		"signing_cert", caCert, "signing_private_key", caKey,
		"CN", "web.example.com",
		"subject_alt_names", []any{"web.example.com", "IP:10.0.0.1", "admin@example.com"},
		"ext_key_usage", []any{"serverAuth"}))

	// The CA signed it, and a different CA did not.
	if got := x509Call(t, r, "x509.verify_signature",
		value.MapOf("certificate", leafCert, "signing_cert", caCert)); got != true {
		t.Error("the leaf should verify against its CA")
	}
	if got := x509Call(t, r, "x509.verify_signature",
		value.MapOf("certificate", leafCert, "signing_cert", leafCert)); got != false {
		t.Error("the leaf should not verify against itself")
	}

	fields := x509Call(t, r, "x509.read_certificate", value.MapOf("certificate", leafCert)).(*value.Map)
	if cn, _ := fields.Get("common_name"); cn != "web.example.com" {
		t.Errorf("common_name = %#v", cn)
	}
	if ca, _ := fields.Get("is_ca"); ca != false {
		t.Errorf("the leaf should not be a CA")
	}
	if ss, _ := fields.Get("self_signed"); ss != false {
		t.Errorf("the leaf is not self-signed")
	}
	sans, _ := fields.Get("subject_alt_names")
	list, _ := sans.([]any)
	want := map[string]bool{"DNS:web.example.com": true, "IP:10.0.0.1": true, "email:admin@example.com": true}
	if len(list) != len(want) {
		t.Fatalf("subject_alt_names = %v", list)
	}
	for _, s := range list {
		if !want[s.(string)] {
			t.Errorf("unexpected alt name %v", s)
		}
	}
	// The fingerprint is what an operator compares by eye.
	fp, _ := fields.Get("sha256_fingerprint")
	if s, _ := fp.(string); len(s) != 95 || !strings.Contains(s, ":") {
		t.Errorf("sha256_fingerprint = %q", fp)
	}
}

// A CA that is not a CA cannot sign, and the message says so rather than
// producing a certificate nothing will trust.
func TestSigningWithANonCAIsRefused(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := filepath.Join(dir, "k.pem")
	leaf := filepath.Join(dir, "leaf.pem")
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algorithm", "ec"))
	x509Call(t, r, "x509.create_certificate", value.MapOf("path", leaf, "private_key", key, "CN", "not-a-ca"))

	_, err := r.Exec.Call(newCtx(false), "x509.create_certificate", value.MapOf(
		"private_key", key, "signing_cert", leaf, "signing_private_key", key, "CN", "x"))
	if err == nil {
		t.Fatal("a non-CA should not be able to sign")
	}
	if !strings.Contains(err.Error(), "not a CA") {
		t.Errorf("the error should say why: %v", err)
	}

	// A real CA, named without its key, is refused too, rather than
	// silently self-signing and looking like it worked.
	ca := filepath.Join(dir, "ca.pem")
	x509Call(t, r, "x509.create_certificate",
		value.MapOf("path", ca, "private_key", key, "CN", "ca", "ca", true))
	_, err = r.Exec.Call(newCtx(false), "x509.create_certificate", value.MapOf(
		"private_key", key, "signing_cert", ca, "CN", "x"))
	if err == nil || !strings.Contains(err.Error(), "signing_private_key") {
		t.Errorf("a CA named without its key should be refused: %v", err)
	}
}

func TestCSRRoundTrip(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := filepath.Join(dir, "k.pem")
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algorithm", "ec"))

	csrPEM := x509Call(t, r, "x509.create_csr", value.MapOf(
		"private_key", key, "CN", "req.example.com",
		"subject_alt_names", []any{"req.example.com", "alt.example.com"})).(string)

	fields := x509Call(t, r, "x509.read_csr", value.MapOf("csr", csrPEM)).(*value.Map)
	if cn, _ := fields.Get("common_name"); cn != "req.example.com" {
		t.Errorf("common_name = %#v", cn)
	}

	// A request whose signature does not verify is not a request; it is a
	// name someone else chose.
	block, _ := pem.Decode([]byte(csrPEM))
	block.Bytes[len(block.Bytes)-1] ^= 0xff
	tampered := string(pem.EncodeToMemory(block))
	if _, err := r.Exec.Call(newCtx(false), "x509.read_csr", value.MapOf("csr", tampered)); err == nil {
		t.Error("a tampered CSR should be refused")
	}
}

func TestExpiresAndPublicKey(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := filepath.Join(dir, "k.pem")
	cert := filepath.Join(dir, "c.pem")
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algorithm", "ec"))
	x509Call(t, r, "x509.create_certificate",
		value.MapOf("path", cert, "private_key", key, "CN", "x", "days_valid", int64(10)))

	if got := x509Call(t, r, "x509.expires", value.MapOf("certificate", cert, "days", int64(5))); got != false {
		t.Error("a certificate with 10 days left does not expire within 5")
	}
	if got := x509Call(t, r, "x509.expires", value.MapOf("certificate", cert, "days", int64(30))); got != true {
		t.Error("a certificate with 10 days left does expire within 30")
	}

	// The public key is the same whichever of the three carries it.
	fromKey := x509Call(t, r, "x509.get_public_key", value.MapOf("source", key)).(string)
	fromCert := x509Call(t, r, "x509.get_public_key", value.MapOf("source", cert)).(string)
	if fromKey != fromCert {
		t.Errorf("the certificate's public key differs from its private key's:\n%s\n%s", fromKey, fromCert)
	}
}

func TestEncryptedKeyIsRefusedClearly(t *testing.T) {
	// An encrypted key is a real thing to find on disk, and "no PEM
	// block" would send the reader looking for the wrong problem.
	enc := string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("x")}))
	_, err := loadPrivateKey(enc)
	if err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("error = %v, want one naming the encryption", err)
	}
}

func TestSANParsing(t *testing.T) {
	dns, ips, emails, uris, err := parseSANs([]string{
		"plain.example.com", "DNS:tagged.example.com", "10.0.0.1", "IP:192.168.0.1",
		"user@example.com", "email:other@example.com", "URI:https://example.com/x", "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dns) != 2 || len(ips) != 2 || len(emails) != 2 || len(uris) != 1 {
		t.Errorf("dns=%v ips=%v emails=%v uris=%v", dns, ips, emails, uris)
	}
	if _, _, _, _, err := parseSANs([]string{"IP:not-an-address"}); err == nil {
		t.Error("a malformed IP should be refused rather than becoming a DNS name")
	}
}

func TestKeyUsageParsing(t *testing.T) {
	// The several spellings a tree may use all mean the same thing.
	for _, name := range []string{"digitalSignature", "digital_signature", "Digital Signature"} {
		u, err := parseKeyUsage([]string{name})
		if err != nil || u != x509.KeyUsageDigitalSignature {
			t.Errorf("%q -> %v %v", name, u, err)
		}
	}
	if _, err := parseKeyUsage([]string{"nonsense"}); err == nil {
		t.Error("an unknown key usage should be refused, not ignored")
	}
	if _, err := parseExtKeyUsage([]string{"nonsense"}); err == nil {
		t.Error("an unknown extended key usage should be refused")
	}
}

func TestDefaultKeyUsageFollowsThePurpose(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := filepath.Join(dir, "k.pem")
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algorithm", "ec"))

	caPEM := x509Call(t, r, "x509.create_certificate",
		value.MapOf("private_key", key, "CN", "ca", "ca", true)).(string)
	ca, err := loadCertificate(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	if ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("a CA with no stated key usage should still be able to sign certificates")
	}
	if !ca.MaxPathLenZero {
		t.Error("a CA should carry a path length, so it cannot mint further CAs unnoticed")
	}

	leafPEM := x509Call(t, r, "x509.create_certificate",
		value.MapOf("private_key", key, "CN", "leaf")).(string)
	leaf, _ := loadCertificate(leafPEM)
	if leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Error("a leaf certificate should not be able to sign certificates")
	}
}

func TestSerialsAreUnguessable(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := filepath.Join(dir, "k.pem")
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algorithm", "ec"))

	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		pemText := x509Call(t, r, "x509.create_certificate",
			value.MapOf("private_key", key, "CN", "x")).(string)
		cert, err := loadCertificate(pemText)
		if err != nil {
			t.Fatal(err)
		}
		s := cert.SerialNumber.String()
		if seen[s] {
			t.Fatalf("serial %s was issued twice", s)
		}
		seen[s] = true
		if cert.SerialNumber.BitLen() < 96 {
			t.Errorf("serial %s is only %d bits; a guessable serial is a certificate an attacker can predict",
				s, cert.SerialNumber.BitLen())
		}
	}
}

func TestRSAKeyIsTheRequestedSize(t *testing.T) {
	r := New()
	pemText := x509Call(t, r, "x509.create_private_key",
		value.MapOf("algorithm", "rsa", "bits", int64(2048))).(string)
	key, err := loadPrivateKey(pemText)
	if err != nil {
		t.Fatal(err)
	}
	rk, ok := key.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("key = %T", key)
	}
	if rk.N.BitLen() != 2048 {
		t.Errorf("key is %d bits, want 2048", rk.N.BitLen())
	}
}

// The failure people actually hit with Salt's x509 is a certificate state
// that re-issues on every highstate: a re-issued certificate has a new
// serial and a new expiry, so it never matches what the last run left and
// the run never converges. These states answer the question before they
// act, so a second run does nothing.
func TestX509StatesConvergeOnASecondRun(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := filepath.Join(dir, "key.pem")
	cert := filepath.Join(dir, "cert.pem")

	keyArgs := value.MapOf("name", key, "algorithm", "ec", "curve", "p256")
	certArgs := func() *value.Map {
		return value.MapOf("name", cert, "private_key", key, "CN", "web.example.com",
			"days_valid", int64(365), "days_remaining", int64(30))
	}

	// First run: both change.
	res, err := r.States.Call(newCtx(false), "x509.private_key_managed", keyArgs)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("the first run should create the key: %+v", res)
	}
	res, err = r.States.Call(newCtx(false), "x509.certificate_managed", certArgs())
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("the first run should create the certificate: %+v", res)
	}

	before, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}

	// Second run: neither changes, and the certificate on disk is
	// untouched — same serial, same expiry.
	for _, fn := range []struct {
		name string
		args *value.Map
	}{{"x509.private_key_managed", keyArgs}, {"x509.certificate_managed", certArgs()}} {
		res, err := r.States.Call(newCtx(false), fn.name, fn.args)
		if err != nil {
			t.Fatal(err)
		}
		if res.HasChanges() {
			t.Errorf("%s changed on a converged run: %+v", fn.name, res)
		}
		if !res.Succeeded() {
			t.Errorf("%s failed on a converged run: %s", fn.name, res.Comment)
		}
	}
	after, _ := os.ReadFile(cert)
	if string(before) != string(after) {
		t.Error("the certificate was rewritten on a converged run; its serial and expiry would change every highstate")
	}

	// Test mode against a converged pair predicts nothing.
	res, _ = r.States.Call(newCtx(true), "x509.certificate_managed", certArgs())
	if res.Result == nil || res.HasChanges() {
		t.Errorf("test mode on a converged certificate should be a plain success: %+v", res)
	}
}

func TestPrivateKeyManagedNoticesTheWrongKey(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "key.pem")

	// An RSA key where the tree asked for EC is not the tree's key.
	if _, err := r.States.Call(newCtx(false), "x509.private_key_managed",
		value.MapOf("name", path, "algorithm", "rsa", "bits", int64(2048))); err != nil {
		t.Fatal(err)
	}
	res, err := r.States.Call(newCtx(true), "x509.private_key_managed",
		value.MapOf("name", path, "algorithm", "ec", "curve", "p256"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Fatalf("test mode should predict a replacement: %+v", res)
	}
	if !strings.Contains(res.Comment, "rsa 2048") {
		t.Errorf("the comment should name what is there: %q", res.Comment)
	}
	// Test mode changed nothing: the RSA key is still on disk.
	key, _ := loadPrivateKey(path)
	if describeKey(key) != "rsa 2048" {
		t.Errorf("test mode replaced the key; it is now %s", describeKey(key))
	}

	// An RSA key of the wrong size is also wrong.
	res, _ = r.States.Call(newCtx(true), "x509.private_key_managed",
		value.MapOf("name", path, "algorithm", "rsa", "bits", int64(3072)))
	if res.Result != nil {
		t.Errorf("a 2048-bit key where 3072 was asked for should be replaced: %+v", res)
	}
}

// A certificate whose private key has been replaced is not the tree's
// certificate, whatever else is right about it.
func TestCertificateManagedNoticesAKeyChange(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := filepath.Join(dir, "key.pem")
	cert := filepath.Join(dir, "cert.pem")
	certArgs := value.MapOf("name", cert, "private_key", key, "CN", "x", "days_remaining", int64(0))

	r.States.Call(newCtx(false), "x509.private_key_managed", value.MapOf("name", key, "algorithm", "ec"))
	r.States.Call(newCtx(false), "x509.certificate_managed", certArgs)

	// Replace the key underneath it.
	r.States.Call(newCtx(false), "x509.private_key_managed",
		value.MapOf("name", key, "algorithm", "ec", "new", true))

	res, err := r.States.Call(newCtx(true), "x509.certificate_managed", certArgs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Fatalf("the certificate no longer matches its key and should be re-issued: %+v", res)
	}
	if !strings.Contains(res.Comment, "private key") {
		t.Errorf("the comment should say why: %q", res.Comment)
	}
}

// The renewal window is the whole point of the state: a certificate close
// to expiry is re-issued before it lapses, and one that is not is left
// alone.
func TestCertificateManagedRenewsBeforeExpiry(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := filepath.Join(dir, "key.pem")
	cert := filepath.Join(dir, "cert.pem")
	r.States.Call(newCtx(false), "x509.private_key_managed", value.MapOf("name", key, "algorithm", "ec"))
	r.States.Call(newCtx(false), "x509.certificate_managed", value.MapOf(
		"name", cert, "private_key", key, "CN", "x", "days_valid", int64(10), "days_remaining", int64(0)))

	// A 30-day window against a 10-day certificate: re-issue.
	res, _ := r.States.Call(newCtx(true), "x509.certificate_managed", value.MapOf(
		"name", cert, "private_key", key, "CN", "x", "days_remaining", int64(30)))
	if res.Result != nil {
		t.Errorf("a certificate inside the renewal window should be re-issued: %+v", res)
	}
	if !strings.Contains(res.Comment, "expires") {
		t.Errorf("the comment should say why: %q", res.Comment)
	}

	// A 5-day window: leave it.
	res, _ = r.States.Call(newCtx(true), "x509.certificate_managed", value.MapOf(
		"name", cert, "private_key", key, "CN", "x", "days_remaining", int64(5)))
	if res.Result == nil || res.HasChanges() {
		t.Errorf("a certificate outside the renewal window should be left alone: %+v", res)
	}
}
