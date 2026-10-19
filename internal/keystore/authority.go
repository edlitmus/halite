package keystore

import (
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// DefaultLifetime is SPEC 7.4's ninety days.
const DefaultLifetime = 90 * 24 * time.Hour

// RenewalFraction is the point in a certificate's life at which a node
// renews, per SPEC 7.4. Short credentials only bound a stolen key's use
// if renewal actually happens, so it happens early and often.
const RenewalFraction = 0.5

// Mode is the enrollment mode of SPEC 7.3.
type Mode string

const (
	// ModeManual holds a request in pending until an operator accepts
	// it. The default, and the reason there is no auto_accept.
	ModeManual Mode = "manual"
	// ModeToken issues on a valid, unexpired, unspent bootstrap token.
	ModeToken Mode = "token"
	// ModeAttested issues on a verified cloud or TPM attestation.
	ModeAttested Mode = "attested"
)

// ParseMode reads a configured enrollment mode.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeManual, ModeToken:
		return Mode(s), nil
	case "":
		return ModeManual, nil
	case ModeAttested:
		// Refused rather than accepted, because accepting it would be a
		// lie of exactly the kind this mode exists to prevent. There is
		// no attestation verification in this build — no instance
		// identity document, no TPM, no metadata service — so a hub
		// configured this way took every request through the manual
		// path while the operator believed a machine identity was being
		// checked. Failing safe is not the same as doing what was
		// asked.
		return "", fmt.Errorf("enrollment mode %q is not built: this build verifies no "+
			"attestation of any kind, so it would hold every request for an operator "+
			"while appearing not to. Use %q, or %q with a bootstrap token",
			ModeAttested, ModeManual, ModeToken)
	}
	// Named explicitly, because an operator migrating from Salt will
	// look for this setting and there is deliberately not one.
	if s == "auto" || s == "auto_accept" {
		return "", fmt.Errorf("there is no automatic acceptance mode; use %q, which is accountable", ModeToken)
	}
	return "", fmt.Errorf("%q is not an enrollment mode; use %q or %q", s, ModeManual, ModeToken)
}

// Revoker is the handshake-time denylist of SPEC 7.4, as the authority
// needs it. The transport supplies one.
//
// Revoked is on it because a handshake is not the only moment that
// matters: an HTTP/2 connection established before a revocation stays
// up and carries requests, so whatever serves those requests has to be
// able to ask.
type Revoker interface {
	Revoke(serial, reason string)
	Allow(serial string)
	Revoked(serial string) (string, bool)
}

// Authority decides who joins. It owns the store, the CA, and the
// denylist together, because those three going out of step is what a
// revocation that does not take effect is made of.
type Authority struct {
	Store    *Store
	CA       *pki.CA
	Mode     Mode
	Lifetime time.Duration
	Revoker  Revoker
	// Now is the clock, so a test can watch a certificate expire.
	Now func() time.Time
	// MaxPending bounds the pending records an unauthenticated caller
	// can create; zero means DefaultMaxPending. See Enroll.
	MaxPending int
}

// MaxCSRBytes bounds the PEM CSR Enroll will read. A node's CSR here is
// an ECDSA key, a subject and one SAN, and halite-node's are about 500
// bytes at P-256 and 600 at P-384; 16 KiB leaves room for a CSR another
// tool made and none for the 8 MiB extension issue 321's M-06 stored.
const MaxCSRBytes = 16 << 10

// DefaultMaxPending is how many pending records may exist before a new
// name is refused. Every new name on /v1/enroll wrote one, and nothing
// bounded how many: the endpoint takes no credential, because it is how
// a node with none gets one (issue 321, M-06). An estate enrolling more
// than this at once in manual mode accepts in batches, which `keys
// accept --all` does.
const DefaultMaxPending = 1024

func (a *Authority) maxPending() int {
	if a.MaxPending > 0 {
		return a.MaxPending
	}
	return DefaultMaxPending
}

func (a *Authority) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Authority) lifetime() time.Duration {
	if a.Lifetime > 0 {
		return a.Lifetime
	}
	return DefaultLifetime
}

// ErrPending is returned to a node whose request has not been decided.
// It is not a failure: the node waits and asks again.
var ErrPending = errors.New("the enrollment request is waiting for an operator")

// ErrRefused is returned to a node the hub will not issue to. It is
// deliberately the same error for rejected, revoked, and "a different
// key already holds this name": a node that is not getting in learns
// that it is not getting in.
var ErrRefused = errors.New("enrollment refused")

// ErrMalformed marks a request the hub could not read: a CSR too large,
// not PEM, not a CSR, unsigned, or naming no node. It is the caller's own
// bytes described back to it, so the hub sends the text to the node.
//
// Every other error out of Enroll is the hub's own -- a record it cannot
// read, a disk it cannot write, a CA that will not sign -- and the hub
// must not send that text to a caller nobody has authenticated: it names
// paths under the key store and how they failed. It did, as a 400, and a
// 400 also told `enroll --force` the hub had refused its new key for
// good, so the node deleted it (DIVERGENCE 5.400).
var ErrMalformed = errors.New("the enrollment request is malformed")

// Request is what arrives at /v1/enroll.
type Request struct {
	// CSR is PEM.
	CSR []byte
	// Token is a bootstrap token secret, for ModeToken.
	Token string
	// RemoteAddr is the peer address, for a token's source scope.
	RemoteAddr string
}

// Result is what the hub answers with.
type Result struct {
	NodeID string
	State  State
	// Cert is PEM, present once the request is accepted.
	Cert []byte
	// CABundle is PEM, so a node can pin what issued to it.
	CABundle []byte
	// Fingerprint is the request's public key digest, which is what an
	// operator compares out of band.
	Fingerprint string
	// Superseded is the fingerprint of a pending request for the same
	// name that a spent token replaced, so the hub can say so. Empty
	// otherwise.
	Superseded string
	// Recorded is true when this request wrote a pending record that was
	// not there before, which is what the hub's per-source enrollment
	// limit charges for; asking again about one is not.
	Recorded bool
}

// Enroll records a request and, in an automatic mode, issues.
//
// It is idempotent for the same key: a node that retries because it
// lost the answer gets the same answer, and does not spend a second
// token doing it.
func (a *Authority) Enroll(req Request) (*Result, error) {
	if len(req.CSR) > MaxCSRBytes {
		return nil, fmt.Errorf("%w: the CSR is %d bytes, and a node's is under %d", ErrMalformed, len(req.CSR), MaxCSRBytes)
	}
	csr, err := pki.DecodeCSR(req.CSR)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	nodeID, err := pki.NodeIDFromCSR(csr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	fingerprint, err := pki.FingerprintCSR(csr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	now := a.now()

	// One critical section from the read to the write, spending the
	// token inside it, so that an operator's decision on this node from
	// another process lands wholly before or wholly after.
	var res *Result
	err = a.Store.locked(func() error {
		var err error
		res, err = a.enroll(nodeID, fingerprint, csr, req, now)
		return err
	})
	return res, err
}

// enroll is Enroll's decision, run under the store's lock.
func (a *Authority) enroll(nodeID, fingerprint string, csr *x509.CertificateRequest, req Request, now time.Time) (*Result, error) {
	rec, err := a.Store.get(nodeID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	a.Store.inWindow("enroll", nodeID)

	if rec != nil && rec.Fingerprint != fingerprint && rec.State == Pending &&
		a.Mode == ModeToken && req.Token != "" {
		// **A spent token outranks a pending request.** Nobody has
		// decided a pending record, and anybody can make one: a request
		// for web1 with a bogus token used to leave web1 pending under
		// the caller's key, and then the real web1, arriving with a
		// valid token scoped to it, was refused as "a different key"
		// until an operator deleted the record (issue 321, M-06). A
		// token that spends for this name is the operator's decision
		// made in advance, so it takes the name, and the record it
		// replaces is named by its fingerprint in the hub's log. A token that does not
		// spend changes nothing: the pending record stays.
		tok, spendErr := a.Store.spendToken(req.Token, nodeID, req.RemoteAddr, now)
		if spendErr != nil {
			return nil, fmt.Errorf("%w: %s is pending under a different key (%s), and this request's token does not admit it: %v",
				ErrRefused, rec.NodeID, rec.Fingerprint, spendErr)
		}
		superseded := rec.Fingerprint
		rec = &Record{
			NodeID:      nodeID,
			State:       Pending,
			Source:      SourceToken,
			TokenID:     tok.ID,
			Fingerprint: fingerprint,
			CSR:         string(pki.EncodeCSR(csr.Raw)),
			FirstSeen:   now,
			Updated:     now,
		}
		res, err := a.issue(rec, csr, now)
		if res != nil {
			res.Superseded = superseded
		}
		return res, err
	}

	isNew := rec == nil
	if rec != nil {
		// A different key presenting an established name is the attack
		// this section exists to stop, and it is refused in every
		// state rather than treated as a new request -- except the one
		// above, a pending record and a token that spends.
		if rec.Fingerprint != fingerprint {
			return nil, fmt.Errorf("%w: %s is already known by a different key (%s); delete the record first if the node was rebuilt",
				ErrRefused, rec.NodeID, rec.Fingerprint)
		}
		switch rec.Status(now) {
		case Accepted:
			return a.accepted(rec), nil
		case Expired:
			// The key is the same and the record was accepted once, so
			// this is a node coming back after being off for longer
			// than a certificate lives. That is a renewal.
			return a.issue(rec, csr, now)
		case Pending:
			// Fall through: a pending request may still be carrying a
			// token, and a first attempt refused for its source or its
			// scope must not bar the retry that gets it right.
			rec.Updated = now
		default:
			return nil, fmt.Errorf("%w: %s is %s", ErrRefused, rec.NodeID, rec.State)
		}
	} else {
		// A new name, from a caller nobody has authenticated. A token
		// that spends issues below without counting; anything that would
		// stay pending is bounded, so the store cannot be filled.
		if !(a.Mode == ModeToken && req.Token != "") {
			if err := a.roomForPending(); err != nil {
				return nil, err
			}
		}
		rec = &Record{
			NodeID:      nodeID,
			State:       Pending,
			Source:      SourceManual,
			Fingerprint: fingerprint,
			CSR:         string(pki.EncodeCSR(csr.Raw)),
			FirstSeen:   now,
			Updated:     now,
		}
	}

	if a.Mode == ModeToken && req.Token != "" {
		tok, spendErr := a.Store.spendToken(req.Token, nodeID, req.RemoteAddr, now)
		if spendErr == nil {
			rec.Source = SourceToken
			rec.TokenID = tok.ID
			return a.issue(rec, csr, now)
		}
		// The request is still recorded as pending, so that a failed
		// automatic enrollment leaves something for an operator to
		// accept by hand rather than vanishing -- within the bound, for
		// a new name, since a bad token costs the caller nothing.
		if isNew {
			if err := a.roomForPending(); err != nil {
				return nil, fmt.Errorf("%w; and %v", err, spendErr)
			}
		}
		if err := a.Store.put(rec); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrRefused, spendErr)
	}

	if err := a.Store.put(rec); err != nil {
		return nil, err
	}
	return &Result{NodeID: nodeID, State: Pending, Fingerprint: fingerprint, Recorded: isNew}, ErrPending
}

// roomForPending refuses a new pending record once there are
// maxPending. It reads every record, which is the store's size and is
// paid only for a name not seen before.
func (a *Authority) roomForPending() error {
	n, err := a.Store.countPending()
	if err != nil {
		return err
	}
	if n >= a.maxPending() {
		return fmt.Errorf("%w: %d enrollment requests are already pending, which is the limit; accept or reject some (`halite-hub keys list --state pending`)",
			ErrRefused, n)
	}
	return nil
}

// accepted is the answer to a node collecting what it has been granted.
func (a *Authority) accepted(rec *Record) *Result {
	return &Result{
		NodeID:      rec.NodeID,
		State:       Accepted,
		Cert:        []byte(rec.Cert),
		CABundle:    a.bundle(),
		Fingerprint: rec.Fingerprint,
	}
}

// issue signs and stores. It is the only place a certificate is made.
//
// The caller holds the store's lock and read rec under it, so rec is
// what is on disk now. A revoked record is refused here as well as by
// every caller: revocation is the one decision that must never be
// undone by a later one, and this is the last point at which a new
// serial could be minted for it. DIVERGENCE 5.323.
func (a *Authority) issue(rec *Record, csr *x509.CertificateRequest, now time.Time) (*Result, error) {
	if rec.State == Revoked {
		return nil, fmt.Errorf("%w: %s was revoked (%s)", ErrRefused, rec.NodeID, rec.Reason)
	}
	der, err := a.CA.IssueNode(csr, rec.NodeID, a.lifetime())
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("the certificate just issued for %s does not parse: %w", rec.NodeID, err)
	}
	// A reissue leaves the old serial valid until it expires, so it is
	// revoked here: a node holds one certificate at a time. It is also
	// written into the record, or the revocation lasts only as long as
	// this process (issue 321, M-05): the denylist is memory, and
	// LoadDenylist rebuilds it from the store at every start.
	var kept []SupersededCert
	for _, s := range rec.Superseded {
		if s.Live(now) {
			kept = append(kept, s)
		}
	}
	if rec.Serial != "" && rec.Serial != pki.SerialString(cert) {
		old := SupersededCert{Serial: rec.Serial, NotAfter: rec.NotAfter, At: now}
		if old.Live(now) {
			kept = append(kept, old)
		}
		if a.Revoker != nil {
			a.Revoker.Revoke(rec.Serial, ReasonSuperseded)
		}
	}
	rec.Superseded = kept
	rec.State = Accepted
	rec.Cert = string(pki.EncodeCert(der))
	rec.Serial = pki.SerialString(cert)
	rec.NotAfter = cert.NotAfter
	rec.Reason = ""
	rec.Updated = now
	if err := a.Store.put(rec); err != nil {
		return nil, err
	}
	return &Result{
		NodeID:      rec.NodeID,
		State:       Accepted,
		Cert:        []byte(rec.Cert),
		CABundle:    a.bundle(),
		Fingerprint: rec.Fingerprint,
	}, nil
}

func (a *Authority) bundle() []byte {
	if a.CA == nil || a.CA.Cert == nil {
		return nil
	}
	return pki.EncodeCert(a.CA.Cert.Raw)
}

// Accept issues to a pending request. This is what an operator runs
// after comparing the fingerprint out of band.
func (a *Authority) Accept(nodeID string) (*Record, error) {
	var out *Record
	err := a.Store.locked(func() error {
		rec, err := a.Store.get(nodeID)
		if err != nil {
			return err
		}
		a.Store.inWindow("accept", nodeID)
		now := a.now()
		switch rec.Status(now) {
		case Accepted:
			out = rec
			return nil
		case Revoked:
			return fmt.Errorf("%s was revoked (%s); delete the record to let it enrol again", nodeID, rec.Reason)
		}
		if rec.CSR == "" {
			return fmt.Errorf("%s has no certificate request on file to accept", nodeID)
		}
		csr, err := pki.DecodeCSR([]byte(rec.CSR))
		if err != nil {
			return fmt.Errorf("the stored request for %s: %w", nodeID, err)
		}
		if _, err := a.issue(rec, csr, now); err != nil {
			return err
		}
		out = rec
		return nil
	})
	return out, err
}

// Reject refuses a request and keeps it, for the audit.
//
// A revoked node is refused too. Rejected is a weaker word for the same
// outcome to the node, but not to the hub: LoadDenylist puts only
// revoked serials back on the denylist at start-up, so rejecting a
// revoked record would quietly let its certificate back in at the next
// restart. Revoked is sticky; delete is the way out of it.
func (a *Authority) Reject(nodeID, reason string) (*Record, error) {
	var out *Record
	err := a.Store.locked(func() error {
		rec, err := a.Store.get(nodeID)
		if err != nil {
			return err
		}
		a.Store.inWindow("reject", nodeID)
		switch rec.Status(a.now()) {
		case Accepted:
			return fmt.Errorf("%s is accepted; revoke it rather than rejecting it, so the serial reaches the denylist", nodeID)
		case Revoked:
			return fmt.Errorf("%s was revoked (%s); it stays revoked, so that its serial stays on the denylist", nodeID, rec.Reason)
		}
		rec.State = Rejected
		rec.Reason = reason
		rec.Updated = a.now()
		if err := a.Store.put(rec); err != nil {
			return err
		}
		out = rec
		return nil
	})
	return out, err
}

// Revoke withdraws an acceptance. The serial reaches the handshake
// denylist before the record is written, so the window in which a
// revoked certificate still works is not one that involves a disk.
//
// The serial is the one on disk under the store's lock, so it is the
// node's current certificate and not one a renewal has just replaced.
// Before DIVERGENCE 5.323 a renewal from the hub could write between
// this read and this write -- or after it -- and leave the node accepted
// with a fresh serial that nothing had denylisted.
func (a *Authority) Revoke(nodeID, reason string) (*Record, error) {
	var out *Record
	err := a.Store.locked(func() error {
		rec, err := a.Store.get(nodeID)
		if err != nil {
			return err
		}
		if rec.Serial == "" {
			return fmt.Errorf("%s holds no certificate to revoke", nodeID)
		}
		a.Store.inWindow("revoke", nodeID)
		if a.Revoker != nil {
			a.Revoker.Revoke(rec.Serial, reason)
		}
		rec.State = Revoked
		rec.Reason = reason
		rec.Updated = a.now()
		if err := a.Store.put(rec); err != nil {
			return err
		}
		out = rec
		return nil
	})
	return out, err
}

// Delete removes a node's record and denies every certificate it named
// that has not yet expired: the current one, whatever the record's state,
// and the ones its renewals replaced. They are kept on disk apart from
// the records (DeletedCert), which is what lets a restart, and a hub
// that did not do the deleting, deny them too.
//
// This replaces a revoke-then-delete in each caller. The revocation was
// a state on the record that the delete then removed, so it lasted as
// long as the denylist of whichever process ran it: for the CLI, until
// the command exited, which meant a running hub never heard of it at all.
//
// Entries already expired are pruned here, because a delete is the one
// operation that adds to them.
func (a *Authority) Delete(nodeID string) ([]DeletedCert, error) {
	var kept []DeletedCert
	err := a.Store.locked(func() error {
		now := a.now()
		var err error
		kept, err = a.Store.delete(nodeID, now)
		if err != nil {
			return err
		}
		if a.Revoker != nil {
			for _, d := range kept {
				if d.Live(now) {
					a.Revoker.Revoke(d.Serial, d.Reason)
				}
			}
		}
		return a.Store.pruneDeleted(now)
	})
	return kept, err
}

// Renew reissues to a node that already holds a valid certificate,
// without an operator. SPEC 7.4: renewal needs no token and no
// decision, because the node has already authenticated as itself.
//
// The caller passes the certificate the peer authenticated with, not
// one from the request body.
//
// The check that the record is still accepted and the write of the new
// serial are one critical section, against the CLI as well as the hub's
// own goroutines, so a revocation lands wholly before it (and the renewal
// is refused) or wholly after it (and revokes the new serial).
// DIVERGENCE 5.323.
func (a *Authority) Renew(peer *x509.Certificate, csrPEM []byte) (*Result, error) {
	nodeID, err := pki.NodeIDFromCert(peer)
	if err != nil {
		return nil, err
	}
	csr, err := pki.DecodeCSR(csrPEM)
	if err != nil {
		return nil, err
	}
	// The renewal may carry a new key -- that is most of the point --
	// but it may not carry a new name.
	asked, err := pki.NodeIDFromCSR(csr)
	if err != nil {
		return nil, err
	}
	if asked != nodeID {
		return nil, fmt.Errorf("%w: %s asked to renew as %s", ErrRefused, nodeID, asked)
	}
	fingerprint, err := pki.FingerprintCSR(csr)
	if err != nil {
		return nil, err
	}
	var res *Result
	err = a.Store.locked(func() error {
		rec, err := a.Store.get(nodeID)
		if err != nil {
			return err
		}
		a.Store.inWindow("renew", nodeID)
		if rec.State != Accepted {
			return fmt.Errorf("%w: %s is %s", ErrRefused, nodeID, rec.State)
		}
		if rec.Serial != pki.SerialString(peer) {
			return fmt.Errorf("%w: %s presented serial %s and the hub issued %s",
				ErrRefused, nodeID, pki.SerialString(peer), rec.Serial)
		}
		rec.Fingerprint = fingerprint
		rec.CSR = string(pki.EncodeCSR(csr.Raw))
		res, err = a.issue(rec, csr, a.now())
		return err
	})
	return res, err
}

// ReasonSuperseded is the denylist's reason for a serial a renewal
// replaced.
const ReasonSuperseded = "superseded by a renewal"

// LoadDenylist puts every revoked serial on the handshake denylist, and
// every superseded or deleted one whose certificate has not yet expired.
// A hub that restarts and forgets what it revoked is a hub that lets a
// revoked node back in, so this runs before the listener opens.
func (a *Authority) LoadDenylist() (int, error) {
	if a.Revoker == nil {
		return 0, nil
	}
	records, err := a.Store.List()
	if err != nil {
		return 0, err
	}
	deleted, err := a.Store.ListDeleted()
	if err != nil {
		return 0, err
	}
	now := a.now()
	n := 0
	for _, d := range deleted {
		if d.Live(now) {
			a.Revoker.Revoke(d.Serial, d.Reason)
			n++
		}
	}
	for _, rec := range records {
		for _, s := range rec.Superseded {
			if s.Live(now) {
				a.Revoker.Revoke(s.Serial, ReasonSuperseded)
				n++
			}
		}
		if rec.State == Revoked && rec.Serial != "" {
			reason := rec.Reason
			if reason == "" {
				reason = "revoked"
			}
			a.Revoker.Revoke(rec.Serial, reason)
			n++
		}
	}
	return n, nil
}

// NeedsRenewal reports whether a certificate has passed the halfway
// point of its life.
func NeedsRenewal(cert *x509.Certificate, now time.Time) bool {
	life := cert.NotAfter.Sub(cert.NotBefore)
	if life <= 0 {
		return true
	}
	return !now.Before(cert.NotBefore.Add(time.Duration(float64(life) * RenewalFraction)))
}
