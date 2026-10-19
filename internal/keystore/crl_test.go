package keystore

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// parsedCRL signs a list and reads it back the way any other reader does.
func parsedCRL(t *testing.T, a *Authority) *x509.RevocationList {
	t.Helper()
	raw, _, _, err := a.CRL(7 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "X509 CRL" {
		t.Fatalf("not a PEM CRL: %q", raw)
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := crl.CheckSignatureFrom(a.CA.Cert); err != nil {
		t.Fatalf("the CRL is not signed by the CA: %v", err)
	}
	return crl
}

func crlSerials(crl *x509.RevocationList) map[string]x509.RevocationListEntry {
	out := map[string]x509.RevocationListEntry{}
	for _, e := range crl.RevokedCertificateEntries {
		out[e.SerialNumber.Text(16)] = e
	}
	return out
}

// everyKind builds a store with one serial from each source of the
// denylist, and returns them.
func everyKind(t *testing.T, a *Authority) (revoked, superseded, deleted, revokedThenDeleted string) {
	t.Helper()
	r := accepted(t, a, "revoked.example")
	if _, err := a.Revoke("revoked.example", "key leaked"); err != nil {
		t.Fatal(err)
	}
	superseded, _ = renewed(t, a, "renewed.example")
	d := accepted(t, a, "deleted.example")
	if _, err := a.Delete("deleted.example"); err != nil {
		t.Fatal(err)
	}
	rd := accepted(t, a, "gone.example")
	if _, err := a.Revoke("gone.example", "stolen"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Delete("gone.example"); err != nil {
		t.Fatal(err)
	}
	return pki.SerialString(r), superseded, pki.SerialString(d), pki.SerialString(rd)
}

// The CRL and the handshake's denylist are two answers to one question,
// and they disagreed: the CRL was built from revoked records alone, so a
// renewed node's old certificate and a deleted node's certificate were
// refused by the hub and accepted by anything reading its CRL.
func TestTheCRLListsWhatARestartedHubDenies(t *testing.T) {
	a, _ := newAuthority(t)
	everyKind(t, a)

	denied := restartedDenylist(t, a).revoked
	listed := crlSerials(parsedCRL(t, a))
	for serial := range denied {
		if _, ok := listed[serial]; !ok {
			t.Errorf("the hub denies %s and the CRL does not list it", serial)
		}
	}
	for serial := range listed {
		if _, ok := denied[serial]; !ok {
			t.Errorf("the CRL lists %s and the hub does not deny it", serial)
		}
	}
	if len(denied) != 4 {
		t.Errorf("the hub denies %d serials, want 4: %v", len(denied), denied)
	}
}

// The reason code says what happened, and a revoked record that was
// deleted keeps the time it was revoked rather than the time of the tidy.
func TestTheCRLSaysWhyAndWhen(t *testing.T) {
	a, _ := newAuthority(t)
	now := time.Now().Truncate(time.Second)
	a.Now = func() time.Time { return now }
	a.CA.Now = a.Now

	revoked := accepted(t, a, "gone.example")
	if _, err := a.Revoke("gone.example", "stolen"); err != nil {
		t.Fatal(err)
	}
	revokedAt := now
	now = now.Add(time.Hour)
	if _, err := a.Delete("gone.example"); err != nil {
		t.Fatal(err)
	}
	superseded, _ := renewed(t, a, "renewed.example")
	deleted := accepted(t, a, "deleted.example")
	if _, err := a.Delete("deleted.example"); err != nil {
		t.Fatal(err)
	}
	// A renewal's old serial is still superseded after its node is
	// deleted, not a deletion: it was replaced before the delete.
	supersededThenDeleted, _ := renewed(t, a, "renewed-gone.example")
	if _, err := a.Delete("renewed-gone.example"); err != nil {
		t.Fatal(err)
	}

	listed := crlSerials(parsedCRL(t, a))
	for _, c := range []struct {
		name, serial string
		reason       int
		at           time.Time
	}{
		{"revoked then deleted", pki.SerialString(revoked), 0, revokedAt},
		{"superseded", superseded, pki.ReasonSuperseded, now},
		{"deleted", pki.SerialString(deleted), pki.ReasonCessationOfOperation, now},
		{"superseded, then deleted", supersededThenDeleted, pki.ReasonSuperseded, now},
	} {
		e, ok := listed[c.serial]
		if !ok {
			t.Errorf("%s: %s is not listed", c.name, c.serial)
			continue
		}
		if e.ReasonCode != c.reason {
			t.Errorf("%s: reason %d, want %d", c.name, e.ReasonCode, c.reason)
		}
		if !e.RevocationTime.Equal(c.at.UTC()) {
			t.Errorf("%s: revoked at %s, want %s", c.name, e.RevocationTime, c.at.UTC())
		}
	}
}

// The number was the count of revoked records plus one, which fell when
// a revoked record was deleted, and would fall again as superseded and
// deleted serials expire out of the list. A reader holding the higher
// number then keeps the older list (RFC 5280 section 5.2.3).
func TestTheCRLNumberNeverFalls(t *testing.T) {
	a, _ := newAuthority(t)
	now := time.Now()
	a.Now = func() time.Time { return now }
	a.CA.Now = a.Now

	everyKind(t, a)
	first := parsedCRL(t, a)
	if first.Number.Int64() < now.Unix() {
		t.Errorf("the first number %s is below the time floor %d, so it may be below a count-based one already handed out",
			first.Number, now.Unix())
	}

	// Everything but the revoked record expires out of the list.
	now = now.Add(2 * DefaultLifetime)
	second := parsedCRL(t, a)
	if len(second.RevokedCertificateEntries) >= len(first.RevokedCertificateEntries) {
		t.Fatalf("the list did not shrink (%d then %d); the test proves nothing",
			len(first.RevokedCertificateEntries), len(second.RevokedCertificateEntries))
	}
	if second.Number.Cmp(first.Number) <= 0 {
		t.Errorf("the list shrank and its number went from %s to %s", first.Number, second.Number)
	}

	// A clock stepped back a day does not take the number with it.
	now = now.Add(-24 * time.Hour)
	third := parsedCRL(t, a)
	if third.Number.Cmp(second.Number) <= 0 {
		t.Errorf("after the clock went back the number went from %s to %s", second.Number, third.Number)
	}
}

// A number file that cannot be read is refused, not restarted from zero.
func TestAnUnreadableCRLNumberIsRefused(t *testing.T) {
	a, _ := newAuthority(t)
	if err := os.WriteFile(filepath.Join(a.Store.Dir(), crlNumberName), []byte("twelve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.CRL(time.Hour); err == nil {
		t.Error("a CRL was signed over a number file that does not hold a number")
	}
}

// The number file sits in the records' directory; it must not be read
// as a node.
func TestTheCRLNumberIsNotARecord(t *testing.T) {
	a, _ := newAuthority(t)
	accepted(t, a, "web1.example")
	if _, _, _, err := a.CRL(time.Hour); err != nil {
		t.Fatal(err)
	}
	records, err := a.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Errorf("List returns %d records, want 1", len(records))
	}
}
