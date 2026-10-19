package keystore

import (
	"errors"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// restartedDenylist is what a hub that starts over this store denies.
func restartedDenylist(t *testing.T, a *Authority) *fakeRevoker {
	t.Helper()
	fresh := &fakeRevoker{revoked: map[string]string{}}
	restarted := &Authority{Store: a.Store, CA: a.CA, Mode: ModeManual, Revoker: fresh, Now: a.Now}
	if _, err := restarted.LoadDenylist(); err != nil {
		t.Fatal(err)
	}
	return fresh
}

// `keys delete` revoked an accepted node and then removed the record
// that said so, so the next start had nothing to reload and the deleted
// node's certificate worked until it expired. The CLI's revocation was
// in the CLI's memory, so a running hub never had it either.
func TestADeletedNodesCertificateStaysDeniedAfterARestart(t *testing.T) {
	a, _ := newAuthority(t)
	old, cur := renewed(t, a, "web1.example")

	if _, err := a.Delete("web1.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.Get("web1.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the record is still there: %v", err)
	}
	if reason, denied := a.Revoker.Revoked(cur); !denied || reason != ReasonDeleted {
		t.Errorf("this process did not deny the deleted node's serial: %v, %q", denied, reason)
	}

	fresh := restartedDenylist(t, a)
	if reason, denied := fresh.Revoked(cur); !denied || reason != ReasonDeleted {
		t.Errorf("after a restart the deleted node's serial %s is accepted: %v, %q", cur, denied, reason)
	}
	if _, denied := fresh.Revoked(old); !denied {
		t.Errorf("after a restart the serial %s its renewal replaced is accepted", old)
	}
}

// Deleting a revoked record was a way to undo the revocation: it was
// the only thing that put the serial back on the denylist at a start.
// The revocation's reason is what the node is told, so it is kept.
func TestDeletingARevokedNodeDoesNotUndoTheRevocation(t *testing.T) {
	a, _ := newAuthority(t)
	cert := accepted(t, a, "web1.example")
	if _, err := a.Revoke("web1.example", "key leaked"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Delete("web1.example"); err != nil {
		t.Fatal(err)
	}
	if reason, denied := restartedDenylist(t, a).Revoked(pki.SerialString(cert)); !denied || reason != "key leaked" {
		t.Errorf("the revoked serial after delete and restart: denied %v, %q", denied, reason)
	}
}

// Store.Delete is exported, and the floor: a caller that goes round the
// Authority must not be able to lose a serial either.
func TestTheStoreKeepsTheSerialsOfARecordItDeletes(t *testing.T) {
	a, _ := newAuthority(t)
	cert := accepted(t, a, "web1.example")
	if err := a.Store.Delete("web1.example"); err != nil {
		t.Fatal(err)
	}
	if _, denied := restartedDenylist(t, a).Revoked(pki.SerialString(cert)); !denied {
		t.Error("a delete through the store lost the serial")
	}
}

// Delete is how a rebuilt machine enrols again under its old name. The
// old certificate stays denied and the new one must not be.
func TestANodeCanEnrolAgainAfterItIsDeleted(t *testing.T) {
	a, _ := newAuthority(t)
	first := accepted(t, a, "web1.example")
	if _, err := a.Delete("web1.example"); err != nil {
		t.Fatal(err)
	}
	second := accepted(t, a, "web1.example")

	fresh := restartedDenylist(t, a)
	if _, denied := fresh.Revoked(pki.SerialString(first)); !denied {
		t.Error("the first certificate was let back in by the second enrolment")
	}
	if _, denied := fresh.Revoked(pki.SerialString(second)); denied {
		t.Error("the node that enrolled again is denied")
	}
}

// What is kept is bounded by the certificates' lives, as for superseded
// serials: once expired, the handshake refuses them without help.
func TestAnExpiredDeletedSerialIsNotLoadedAndIsPruned(t *testing.T) {
	a, _ := newAuthority(t)
	now := time.Now()
	a.Now = func() time.Time { return now }
	a.CA.Now = a.Now

	first := accepted(t, a, "web1.example")
	if _, err := a.Delete("web1.example"); err != nil {
		t.Fatal(err)
	}
	now = first.NotAfter.Add(time.Minute)
	if _, denied := restartedDenylist(t, a).Revoked(pki.SerialString(first)); denied {
		t.Error("an expired deleted serial was loaded onto the denylist")
	}

	// The next delete prunes it.
	accepted(t, a, "web2.example")
	if _, err := a.Delete("web2.example"); err != nil {
		t.Fatal(err)
	}
	kept, err := a.Store.ListDeleted()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range kept {
		if d.Serial == pki.SerialString(first) {
			t.Errorf("the expired serial is still on disk: %+v", kept)
		}
	}
	if len(kept) != 1 || kept[0].NodeID != "web2.example" {
		t.Errorf("kept %+v, want only web2.example's", kept)
	}
}
