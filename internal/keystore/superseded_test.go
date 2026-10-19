package keystore

import (
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// renewed accepts nodeID, renews it once with a fresh key, and returns
// the superseded certificate and the one that replaced it.
func renewed(t *testing.T, a *Authority, nodeID string) (old, cur string) {
	t.Helper()
	first := accepted(t, a, nodeID)
	next, _ := request(t, nodeID)
	res, err := a.Renew(first, next)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pki.DecodeCert(res.Cert)
	if err != nil {
		t.Fatal(err)
	}
	return pki.SerialString(first), pki.SerialString(second)
}

// A renewal denylists the serial it replaces, but the record kept only
// the new one, so a hub that restarted let the old certificate back in
// for the rest of its life: up to half of it, 45 days at the default.
// A key rotated because it leaked was a key that worked again after
// the next restart. Issue 321, M-05.
func TestASupersededSerialStaysDeniedAfterARestart(t *testing.T) {
	a, _ := newAuthority(t)
	old, cur := renewed(t, a, "web1.example")

	fresh := &fakeRevoker{revoked: map[string]string{}}
	restarted := &Authority{Store: a.Store, CA: a.CA, Mode: ModeManual, Revoker: fresh}
	if _, err := restarted.LoadDenylist(); err != nil {
		t.Fatal(err)
	}
	if _, denied := fresh.Revoked(old); !denied {
		t.Errorf("after a restart the superseded serial %s is accepted again", old)
	}
	if _, denied := fresh.Revoked(cur); denied {
		t.Errorf("the current serial %s was denylisted", cur)
	}
}

// What is kept is bounded by the certificates' own lives: a superseded
// serial whose certificate has expired is refused by the handshake
// anyway, so it is dropped at the next issue rather than kept for ever.
func TestAnExpiredSupersededSerialIsDropped(t *testing.T) {
	a, _ := newAuthority(t)
	now := time.Now()
	a.Now = func() time.Time { return now }
	a.CA.Now = a.Now

	// Renewed at half-life, as a node does, so the replacement outlives
	// the certificate it replaced.
	first := accepted(t, a, "web1.example")
	now = now.Add(DefaultLifetime / 2)
	next, _ := request(t, "web1.example")
	if _, err := a.Renew(first, next); err != nil {
		t.Fatal(err)
	}
	old := pki.SerialString(first)

	rec, err := a.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Superseded) != 1 || rec.Superseded[0].Serial != old {
		t.Fatalf("the record keeps %+v, want the one superseded serial %s", rec.Superseded, old)
	}

	// Past the old certificate's notAfter, and renewing again.
	now = rec.Superseded[0].NotAfter.Add(time.Minute)
	cur, err := pki.DecodeCert([]byte(rec.Cert))
	if err != nil {
		t.Fatal(err)
	}
	next, _ = request(t, "web1.example")
	if _, err := a.Renew(cur, next); err != nil {
		t.Fatal(err)
	}
	rec, err = a.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rec.Superseded {
		if s.Serial == old {
			t.Errorf("the expired serial %s is still kept: %+v", old, rec.Superseded)
		}
	}
	if len(rec.Superseded) != 1 || rec.Superseded[0].Serial != pki.SerialString(cur) {
		t.Errorf("the record keeps %+v, want only %s", rec.Superseded, pki.SerialString(cur))
	}

	fresh := &fakeRevoker{revoked: map[string]string{}}
	restarted := &Authority{Store: a.Store, CA: a.CA, Mode: ModeManual, Revoker: fresh, Now: a.Now}
	if _, err := restarted.LoadDenylist(); err != nil {
		t.Fatal(err)
	}
	if _, denied := fresh.Revoked(old); denied {
		t.Errorf("an expired superseded serial was loaded onto the denylist")
	}
}
