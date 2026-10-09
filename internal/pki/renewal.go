package pki

import (
	"crypto/x509"
	"time"
)

// DueForRenewal is the halfway point of SPEC 7.4: a certificate is
// renewed once half its life has passed, so that a renewal that fails
// has the other half to be retried in.
//
// Nodes and the hub both renew by it. The hub did not renew at all: it
// loaded hub.crt once at startup and issued another only when it found
// the one on disk had already expired, so a hub left running served an
// expired certificate from day 90 and every node failed its handshake.
// The rule lives here rather than in either command so that the two
// cannot come to disagree about when renewal is due. DIVERGENCE 5.259.
//
// The halfway point is measured from when the certificate was issued,
// not from NotBefore, which every certificate this package issues sets
// IssueBackdate earlier to absorb clock skew. Measured from NotBefore,
// a certificate shorter than twice the backdate was due before it was
// issued, and a hub with a 30-second `certificate_lifetime` renewed on
// every check, forever. For the 90-day default the difference is thirty
// seconds.
func DueForRenewal(cert *x509.Certificate, now time.Time) bool {
	issued := cert.NotBefore.Add(IssueBackdate)
	if !issued.Before(cert.NotAfter) {
		// Not one of ours, or already over: measure the whole span.
		issued = cert.NotBefore
	}
	life := cert.NotAfter.Sub(issued)
	if life <= 0 {
		return true
	}
	return !now.Before(issued.Add(life / 2))
}

// IssueBackdate is how far before the moment of issue every certificate
// this package issues is valid from, so that a peer whose clock is a
// little behind does not refuse a certificate it has just been given.
const IssueBackdate = time.Minute

// RenewalCheckEvery is how often a renewer looks at a certificate: a
// twentieth of its life, between ten seconds and an hour. An hour is
// ample for the 90-day default, whose renewal falls due on day 45;
// scaling down with the lifetime is what lets a short
// `certificate_lifetime` -- a test, or an estate that wants one -- still
// renew in time, without a setting of its own.
func RenewalCheckEvery(cert *x509.Certificate) time.Duration {
	every := cert.NotAfter.Sub(cert.NotBefore) / 20
	switch {
	case every < 10*time.Second:
		return 10 * time.Second
	case every > time.Hour:
		return time.Hour
	}
	return every
}
