package keystore

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// Accepted is the nodes Record.Status calls accepted, in node ID order:
// not one pending, rejected or revoked, and not one whose certificate has
// expired, which the hub must not send a job to.
func TestAcceptedIsWhatStatusCallsAccepted(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, r := range []*Record{
		{NodeID: "c.example", State: Accepted, NotAfter: now.Add(time.Hour)},
		{NodeID: "a.example", State: Accepted, NotAfter: now.Add(time.Hour)},
		{NodeID: "b.example", State: Accepted}, // no expiry recorded
		{NodeID: "expired.example", State: Accepted, NotAfter: now.Add(-time.Minute)},
		{NodeID: "pending.example", State: Pending},
		{NodeID: "rejected.example", State: Rejected},
		{NodeID: "revoked.example", State: Revoked, NotAfter: now.Add(time.Hour)},
	} {
		r.Fingerprint = "aa"
		if err := s.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Accepted(now)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.example", "b.example", "c.example"}; !slices.Equal(got, want) {
		t.Fatalf("Accepted = %q; want %q", got, want)
	}
	// The same answer List gives, read the long way.
	recs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var viaList []string
	for _, r := range recs {
		if r.Status(now) == Accepted {
			viaList = append(viaList, r.NodeID)
		}
	}
	if !slices.Equal(got, viaList) {
		t.Fatalf("Accepted = %q, List says %q", got, viaList)
	}
}

// Accepted answers from List's kept snapshot, reading no record file
// while nothing changes, and sees a node accepted by another process,
// which is what `halite-hub keys accept` is.
func TestAcceptedSharesListsSnapshot(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		putAccepted(t, s, fmt.Sprintf("n%02d.example", i))
	}
	time.Sleep(listCacheMargin + 200*time.Millisecond)
	listIDs(t, s)
	if _, err := s.Accepted(time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := reads(s); got != 5 {
		t.Fatalf("List then Accepted read %d record files; want 5, Accepted reading none", got)
	}
	other, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	putAccepted(t, other, "late.example")
	got, err := s.Accepted(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "late.example") || len(got) != 6 {
		t.Fatalf("Accepted = %q after another store accepted late.example; want all six", got)
	}
}

// What a dispatch pays for the list of nodes it could send to, at the
// AWS lab's 8,000 records, answered from the kept snapshot.
func benchmarkKept(b *testing.B, n int) *Store {
	b.Helper()
	s, err := Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	for i := range n {
		if err := s.Put(&Record{NodeID: fmt.Sprintf("n%05d.example", i), State: Accepted, Fingerprint: "aa",
			NotAfter: time.Now().Add(time.Hour)}); err != nil {
			b.Fatal(err)
		}
	}
	time.Sleep(listCacheMargin + 200*time.Millisecond)
	if _, err := s.List(); err != nil {
		b.Fatal(err)
	}
	return s
}

func BenchmarkAcceptedIDsViaList(b *testing.B) {
	s := benchmarkKept(b, 8000)
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		recs, err := s.List()
		if err != nil {
			b.Fatal(err)
		}
		ids := make([]string, 0, len(recs))
		for _, r := range recs {
			if r.Status(now) == Accepted {
				ids = append(ids, r.NodeID)
			}
		}
	}
}

func BenchmarkAccepted(b *testing.B) {
	s := benchmarkKept(b, 8000)
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Accepted(now); err != nil {
			b.Fatal(err)
		}
	}
}
