package keystore

import (
	"errors"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// **A renewal's CSR is bounded exactly as an enrollment's is.** The size
// check was on Enroll only, so a node holding a certificate could store
// a CSR of any size in its record by renewing: issue 321's close-out
// stored one of 2.84 MB. Both paths now ask checkCSRSize, and this holds
// them to the same answer at the same byte.
func TestARenewalCSRIsBoundedLikeAnEnrollment(t *testing.T) {
	a, _ := newAuthority(t)
	first := accepted(t, a, "web1.example")
	before, err := a.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}

	big := paddedRequest(t, "web1.example", 1<<20)
	if _, err := a.Renew(first, big); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a %d-byte renewal CSR was not refused as malformed: %v", len(big), err)
	}
	after, err := a.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if after.CSR != before.CSR || after.Serial != before.Serial {
		t.Errorf("the refused renewal changed the record: %d-byte CSR, serial %s -> %s",
			len(after.CSR), before.Serial, after.Serial)
	}

	// The two paths agree at the boundary itself, not merely on a CSR
	// far past it: a valid CSR just over the bound is refused by both,
	// by its size, and one just under it is refused by neither.
	over, under := csrAround(t, "web1.example", MaxCSRBytes)
	if _, err := a.Enroll(Request{CSR: over}); !errors.Is(err, ErrMalformed) {
		t.Errorf("enroll took a %d-byte CSR: %v", len(over), err)
	}
	if _, err := a.Renew(first, over); !errors.Is(err, ErrMalformed) {
		t.Errorf("renew took a %d-byte CSR: %v", len(over), err)
	}
	if _, err := a.Renew(first, under); err != nil {
		t.Fatalf("renew refused a %d-byte CSR, under the %d bound: %v", len(under), MaxCSRBytes, err)
	}
	if _, err := a.Enroll(Request{CSR: under}); err != nil && errors.Is(err, ErrMalformed) {
		t.Errorf("enroll refused a %d-byte CSR, under the %d bound: %v", len(under), MaxCSRBytes, err)
	}
	first, err = pki.DecodeCert([]byte(mustGet(t, a, "web1.example").Cert))
	if err != nil {
		t.Fatal(err)
	}

	// And an ordinary renewal still works afterwards.
	next, _ := request(t, "web1.example")
	if _, err := a.Renew(first, next); err != nil {
		t.Errorf("an ordinary renewal after the refused ones: %v", err)
	}
}

// csrAround returns two valid node CSRs whose PEM is just over and just
// under limit bytes, padded by an extension as paddedRequest does.
func csrAround(t *testing.T, nodeID string, limit int) (over, under []byte) {
	t.Helper()
	pad := limit * 3 / 4
	for i := 0; i < 64 && (over == nil || under == nil); i++ {
		b := paddedRequest(t, nodeID, pad)
		switch n := len(b); {
		case n > limit && n <= limit+256:
			over = b
			pad -= 64
		case n <= limit && n > limit-256:
			under = b
			pad += 64
		default:
			pad += (limit - n) * 3 / 4
		}
	}
	if over == nil || under == nil {
		t.Fatalf("could not make CSRs either side of %d bytes", limit)
	}
	return over, under
}

func mustGet(t *testing.T, a *Authority, nodeID string) *Record {
	t.Helper()
	rec, err := a.Store.Get(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// renewAgain renews whatever certificate the record holds now, with a
// fresh key, as `halite-node renew --force` does.
func renewAgain(t *testing.T, a *Authority, nodeID string) error {
	t.Helper()
	rec, err := a.Store.Get(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := pki.DecodeCert([]byte(rec.Cert))
	if err != nil {
		t.Fatal(err)
	}
	next, _ := request(t, nodeID)
	_, err = a.Renew(cur, next)
	return err
}

// **Renewal frequency is bounded, and no superseded serial is ever let
// go to make room.** A node could renew as often as it liked; each
// renewal kept the serial it replaced for the rest of that certificate's
// life and rewrote the whole record, so a node renewing in a loop grew
// its record without bound and paid for each write by the size of all
// the earlier ones.
//
// The bound is on the live superseded serials, not on how soon after
// issue a renewal may come: a node that rotates a leaked key the day
// after its scheduled renewal must be able to, and it can, many times
// over before this refuses.
func TestRenewalsAreBoundedByLiveSupersededSerials(t *testing.T) {
	a, rev := newAuthority(t)
	now := time.Now()
	a.Now = func() time.Time { return now }
	a.CA.Now = a.Now
	accepted(t, a, "web1.example")

	// A scheduled renewal at half-life, then a rotation the next day:
	// what a node whose key leaked does. Neither is refused.
	now = now.Add(DefaultLifetime / 2)
	if err := renewAgain(t, a, "web1.example"); err != nil {
		t.Fatalf("the scheduled renewal: %v", err)
	}
	now = now.Add(24 * time.Hour)
	if err := renewAgain(t, a, "web1.example"); err != nil {
		t.Fatalf("an early rotation after the scheduled renewal: %v", err)
	}

	// Renewing in a loop stops at the bound.
	var refused error
	renewals := 2
	for i := 0; i < 4*MaxSuperseded && refused == nil; i++ {
		now = now.Add(time.Minute)
		if refused = renewAgain(t, a, "web1.example"); refused == nil {
			renewals++
		}
	}
	if !errors.Is(refused, ErrRefused) {
		t.Fatalf("%d renewals in a row were all taken (last: %v); want a refusal at %d live superseded serials",
			renewals, refused, MaxSuperseded)
	}
	rec, err := a.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Superseded) != MaxSuperseded {
		t.Errorf("the record keeps %d superseded serials at the refusal, want exactly %d", len(rec.Superseded), MaxSuperseded)
	}
	// Every serial a renewal replaced is still denied: the bound refused
	// the next renewal rather than forgetting an old certificate, which
	// would have let a leaked key back in.
	for _, s := range rec.Superseded {
		if _, denied := rev.Revoked(s.Serial); !denied {
			t.Errorf("superseded serial %s is no longer denied", s.Serial)
		}
	}

	// The refusal is not permanent: once the oldest superseded
	// certificate expires, renewal works again.
	oldest := rec.Superseded[0].NotAfter
	for _, s := range rec.Superseded {
		if s.NotAfter.Before(oldest) {
			oldest = s.NotAfter
		}
	}
	now = oldest.Add(time.Minute)
	if err := renewAgain(t, a, "web1.example"); err != nil {
		t.Errorf("renewal is still refused after the oldest superseded certificate expired: %v", err)
	}
}
