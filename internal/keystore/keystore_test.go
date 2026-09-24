package keystore

import (
	"crypto"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

type fakeRevoker struct{ revoked map[string]string }

func (f *fakeRevoker) Revoke(serial, reason string) { f.revoked[serial] = reason }
func (f *fakeRevoker) Allow(serial string)          { delete(f.revoked, serial) }
func (f *fakeRevoker) Revoked(serial string) (string, bool) {
	reason, ok := f.revoked[serial]
	return reason, ok
}

func newAuthority(t *testing.T) (*Authority, *fakeRevoker) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.NewCA(pki.ECDSAP256, "test CA", 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rev := &fakeRevoker{revoked: map[string]string{}}
	return &Authority{Store: store, CA: ca, Mode: ModeManual, Lifetime: DefaultLifetime, Revoker: rev}, rev
}

func request(t *testing.T, nodeID string) ([]byte, crypto.Signer) {
	t.Helper()
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	der, err := pki.NewNodeCSR(key, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return pki.EncodeCSR(der), key
}

func TestAManualRequestWaitsForAnOperator(t *testing.T) {
	a, _ := newAuthority(t)
	csr, _ := request(t, "web1.example")

	res, err := a.Enroll(Request{CSR: csr})
	if !errors.Is(err, ErrPending) {
		t.Fatalf("a manual enrollment should be pending, got %v", err)
	}
	if len(res.Cert) != 0 {
		t.Error("a pending request must not come back with a certificate")
	}
	if res.Fingerprint == "" {
		t.Error("the answer should carry the fingerprint an operator compares")
	}

	// Retrying is not a second request.
	if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrPending) {
		t.Fatalf("a retry should still be pending, got %v", err)
	}
	all, err := a.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("a retry created %d records", len(all))
	}

	rec, err := a.Accept("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != Accepted || rec.Cert == "" {
		t.Fatalf("after accept the record is %s with cert %q", rec.State, rec.Cert)
	}

	// And now the node's next request answers with the certificate.
	res, err = a.Enroll(Request{CSR: csr})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != Accepted || len(res.Cert) == 0 || len(res.CABundle) == 0 {
		t.Error("an accepted node should collect its certificate and the CA")
	}
}

// The attack the section exists to stop.
func TestADifferentKeyCannotTakeAnEstablishedName(t *testing.T) {
	a, _ := newAuthority(t)
	first, _ := request(t, "web1.example")
	if _, err := a.Enroll(Request{CSR: first}); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if _, err := a.Accept("web1.example"); err != nil {
		t.Fatal(err)
	}

	impostor, _ := request(t, "web1.example")
	_, err := a.Enroll(Request{CSR: impostor})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("a second key claiming web1.example got %v", err)
	}
}

func TestRejectionIsKeptAndRefusesLater(t *testing.T) {
	a, _ := newAuthority(t)
	csr, _ := request(t, "stranger.example")
	if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if _, err := a.Reject("stranger.example", "not ours"); err != nil {
		t.Fatal(err)
	}
	rec, err := a.Store.Get("stranger.example")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != Rejected || rec.CSR == "" {
		t.Error("a rejection should keep the request it refused")
	}
	if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrRefused) {
		t.Error("a rejected node should not be able to re-enrol by asking again")
	}
}

func TestRevocationReachesTheHandshakeAndSurvivesARestart(t *testing.T) {
	a, rev := newAuthority(t)
	csr, _ := request(t, "web1.example")
	if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	rec, err := a.Accept("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	serial := rec.Serial

	if _, err := a.Revoke("web1.example", "decommissioned"); err != nil {
		t.Fatal(err)
	}
	if rev.revoked[serial] != "decommissioned" {
		t.Fatalf("the serial did not reach the denylist: %v", rev.revoked)
	}

	// A hub that restarts and forgets what it revoked lets it back in.
	fresh := &fakeRevoker{revoked: map[string]string{}}
	restarted := &Authority{Store: a.Store, CA: a.CA, Mode: ModeManual, Revoker: fresh}
	n, err := restarted.LoadDenylist()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || fresh.revoked[serial] == "" {
		t.Errorf("a restarted hub loaded %d revocations: %v", n, fresh.revoked)
	}
}

func TestATokenAdmitsOnceAndWithinItsScope(t *testing.T) {
	a, _ := newAuthority(t)
	a.Mode = ModeToken
	now := time.Now()
	a.Now = func() time.Time { return now }

	_, secret, err := a.Store.MintToken(TokenOptions{
		TTL:      time.Hour,
		NodeGlob: "web*.example",
		CIDR:     "10.0.0.0/8",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	// Out of the node scope.
	other, _ := request(t, "db1.example")
	if _, err := a.Enroll(Request{CSR: other, Token: secret, RemoteAddr: "10.1.2.3:9000"}); !errors.Is(err, ErrRefused) {
		t.Errorf("db1.example is outside web*.example: %v", err)
	}
	// Out of the source scope.
	web, _ := request(t, "web1.example")
	if _, err := a.Enroll(Request{CSR: web, Token: secret, RemoteAddr: "192.0.2.5:9000"}); !errors.Is(err, ErrRefused) {
		t.Errorf("192.0.2.5 is outside 10.0.0.0/8: %v", err)
	}
	// A failed automatic enrollment still leaves something to accept.
	if rec, err := a.Store.Get("web1.example"); err != nil || rec.State != Pending {
		t.Errorf("a refused token should leave a pending record, got %v %v", rec, err)
	}

	res, err := a.Enroll(Request{CSR: web, Token: secret, RemoteAddr: "10.1.2.3:9000"})
	if err != nil {
		t.Fatalf("in scope and in date, and it was refused: %v", err)
	}
	if res.State != Accepted || len(res.Cert) == 0 {
		t.Fatal("a valid token should issue")
	}

	// Single use by default: a second node cannot spend it.
	web2, _ := request(t, "web2.example")
	if _, err := a.Enroll(Request{CSR: web2, Token: secret, RemoteAddr: "10.1.2.3:9000"}); !errors.Is(err, ErrRefused) {
		t.Error("a single-use token admitted a second node")
	}
}

func TestATokenCannotOutliveADay(t *testing.T) {
	a, _ := newAuthority(t)
	now := time.Now()
	if _, _, err := a.Store.MintToken(TokenOptions{TTL: 0}, now); err == nil {
		t.Error("a token without a lifetime should be refused")
	}
	if _, _, err := a.Store.MintToken(TokenOptions{TTL: 48 * time.Hour}, now); err == nil {
		t.Error("a token lasting two days should be refused")
	}
	tok, secret, err := a.Store.MintToken(TokenOptions{TTL: time.Minute}, now)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Digest == secret || len(tok.Digest) != 64 {
		t.Error("the store should hold a digest, not the secret")
	}
	if _, err := a.Store.SpendToken(secret, "web1.example", "10.0.0.1:1", now.Add(time.Hour)); err == nil {
		t.Error("an expired token should not be spendable")
	}
}

func TestThereIsNoAutoAcceptMode(t *testing.T) {
	if _, err := ParseMode("auto_accept"); err == nil {
		t.Fatal("auto_accept should not name a mode")
	}
	for _, name := range []string{"manual", "token"} {
		if _, err := ParseMode(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// `attested` was accepted, and there is no attestation verification
	// anywhere in this build — no instance identity document, no TPM, no
	// metadata service. The automatic branch fires only for a token, so
	// a hub configured this way sent every request to the manual queue
	// while its operator believed a machine identity was being checked.
	// Failing safe is not the same as doing what was asked, and the
	// documentation said the mode was refused.
	err := errorFrom(ParseMode("attested"))
	if err == nil {
		t.Fatal("attested should be refused while nothing verifies an attestation")
	}
	if !strings.Contains(err.Error(), "not built") {
		t.Errorf("the refusal should say the mode is not built, got: %v", err)
	}
	if m, err := ParseMode(""); err != nil || m != ModeManual {
		t.Errorf("the default should be manual, got %q %v", m, err)
	}
}

func TestRenewalNeedsNoOperatorButKeepsTheName(t *testing.T) {
	a, rev := newAuthority(t)
	csr, _ := request(t, "web1.example")
	if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	rec, err := a.Accept("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	first, err := pki.DecodeCert([]byte(rec.Cert))
	if err != nil {
		t.Fatal(err)
	}

	// A renewal with a fresh key, which is the point of renewing.
	next, _ := request(t, "web1.example")
	res, err := a.Renew(first, next)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pki.DecodeCert(res.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if pki.SerialString(second) == pki.SerialString(first) {
		t.Error("a renewal should issue a new serial")
	}
	if rev.revoked[pki.SerialString(first)] == "" {
		t.Error("the superseded certificate should not stay valid")
	}

	// The renewed certificate is now the one on file, so the old one
	// cannot renew again.
	if _, err := a.Renew(first, next); !errors.Is(err, ErrRefused) {
		t.Errorf("a superseded certificate renewed: %v", err)
	}
	// And a renewal cannot rename the node.
	other, _ := request(t, "db1.example")
	if _, err := a.Renew(second, other); !errors.Is(err, ErrRefused) {
		t.Errorf("a renewal renamed the node: %v", err)
	}
}

func TestRenewalWindowIsHalfTheLifetime(t *testing.T) {
	a, _ := newAuthority(t)
	csr, _ := request(t, "web1.example")
	if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	rec, err := a.Accept("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := pki.DecodeCert([]byte(rec.Cert))
	if err != nil {
		t.Fatal(err)
	}
	if NeedsRenewal(cert, cert.NotBefore.Add(30*24*time.Hour)) {
		t.Error("a thirty-day-old ninety-day certificate does not need renewing")
	}
	if !NeedsRenewal(cert, cert.NotBefore.Add(50*24*time.Hour)) {
		t.Error("a fifty-day-old ninety-day certificate does")
	}
}

// A record past notAfter reports expired without anything having
// rewritten it, so a hub that was switched off does not come back
// believing an old certificate is current.
func TestExpiryIsReadFromTheClock(t *testing.T) {
	a, _ := newAuthority(t)
	a.Lifetime = time.Hour
	csr, _ := request(t, "web1.example")
	if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	rec, err := a.Accept("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Status(time.Now()); got != Accepted {
		t.Fatalf("a fresh certificate reads %s", got)
	}
	if got := rec.Status(rec.NotAfter.Add(time.Second)); got != Expired {
		t.Fatalf("a certificate past notAfter reads %s", got)
	}
	if stored, err := a.Store.Get("web1.example"); err != nil || stored.State != Accepted {
		t.Error("expiry is computed and must not have rewritten the record")
	}
}

func TestTheStoreRefusesAnIdentityThatIsAPath(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../../etc/passwd", "a/b", "..", "", "web1 example"} {
		if _, err := store.Get(bad); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%q was treated as a node identity: %v", bad, err)
		}
	}
}

// errorFrom keeps a two-value call readable in a one-value assertion.
func errorFrom(_ Mode, err error) error { return err }

// A token can be deleted, and deleting one removes its record from disk.
//
// `DeleteToken` was written and nothing called it: `keys token create`,
// `list` and `revoke` existed and `delete` did not, so every token ever
// minted stayed in the store. An autoscaling fleet mints one per instance,
// which is the documented use for tokens, so `keys token list` grew
// without bound.
//
// Revoking and deleting are different answers and the difference is the
// `SpentBy` record: a revoked token stops admitting anything and keeps the
// list of what it let in, which is how a leaked token is answered with a
// list rather than a guess. Deleting destroys that, which is why the
// command says what it is destroying. DIVERGENCE 5.144.
func TestDeletingATokenRemovesItAndRevokingKeepsIt(t *testing.T) {
	a, _ := newAuthority(t)
	a.Mode = ModeToken
	now := time.Now()
	a.Now = func() time.Time { return now }

	spent, secret, err := a.Store.MintToken(TokenOptions{TTL: time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}
	keep, _, err := a.Store.MintToken(TokenOptions{TTL: time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}

	// Spend the first, so that it has something to forget.
	csr, _ := request(t, "web1.example")
	if _, err := a.Enroll(Request{CSR: csr, Token: secret, RemoteAddr: "10.1.2.3:9000"}); err != nil {
		t.Fatalf("enrolling with a live token: %v", err)
	}
	before, err := a.Store.GetToken(spent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.SpentBy) == 0 {
		t.Fatal("the token recorded nothing it admitted, so this test cannot tell " +
			"deleting from revoking")
	}

	// Revoking keeps the record, which is the reason to prefer it.
	if err := a.Store.RevokeToken(keep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.GetToken(keep.ID); err != nil {
		t.Errorf("a revoked token is gone from the store: %v", err)
	}

	// Deleting removes it.
	if err := a.Store.DeleteToken(spent.ID); err != nil {
		t.Fatalf("deleting a spent token: %v", err)
	}
	if _, err := a.Store.GetToken(spent.ID); !errors.Is(err, ErrNoToken) {
		t.Errorf("reading a deleted token returned %v, want ErrNoToken", err)
	}
	tokens, err := a.Store.ListTokens()
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].ID != keep.ID {
		t.Errorf("the store lists %d token(s) after one of two was deleted", len(tokens))
	}

	// Deleting one that is not there says so rather than succeeding.
	if err := a.Store.DeleteToken(spent.ID); !errors.Is(err, ErrNoToken) {
		t.Errorf("deleting a token twice returned %v, want ErrNoToken", err)
	}
}
