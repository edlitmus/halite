package keystore

import (
	"crypto"
	"errors"
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
	for _, name := range []string{"manual", "token", "attested"} {
		if _, err := ParseMode(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
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
