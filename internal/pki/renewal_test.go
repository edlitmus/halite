package pki

import (
	"crypto/x509"
	"testing"
	"time"
)

// A certificate is due half way through its life measured from when it
// was issued, which is IssueBackdate after its NotBefore. Measured from
// NotBefore, a 30-second certificate was due before it was issued and a
// hub with one renewed on every check. DIVERGENCE 5.259.
func TestRenewalFallsDueHalfWayFromIssue(t *testing.T) {
	issued := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ours := func(life time.Duration) *x509.Certificate {
		return &x509.Certificate{NotBefore: issued.Add(-IssueBackdate), NotAfter: issued.Add(life)}
	}
	for _, c := range []struct {
		name string
		cert *x509.Certificate
		at   time.Duration
		due  bool
	}{
		{"30s, just issued", ours(30 * time.Second), 0, false},
		{"30s, 14s in", ours(30 * time.Second), 14 * time.Second, false},
		{"30s, 15s in", ours(30 * time.Second), 15 * time.Second, true},
		{"90d, day 44", ours(90 * 24 * time.Hour), 44 * 24 * time.Hour, false},
		{"90d, day 45", ours(90 * 24 * time.Hour), 45 * 24 * time.Hour, true},
		{"expired", ours(time.Hour), 2 * time.Hour, true},
		// Not one of ours: valid from the moment of issue, so the
		// backdate is not taken off its span.
		{"foreign 20s, 9s in", &x509.Certificate{NotBefore: issued, NotAfter: issued.Add(20 * time.Second)}, 9 * time.Second, false},
		{"foreign 20s, 10s in", &x509.Certificate{NotBefore: issued, NotAfter: issued.Add(20 * time.Second)}, 10 * time.Second, true},
	} {
		if got := DueForRenewal(c.cert, issued.Add(c.at)); got != c.due {
			t.Errorf("%s: due = %v, want %v", c.name, got, c.due)
		}
	}
}
