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
}

// Enroll records a request and, in an automatic mode, issues.
//
// It is idempotent for the same key: a node that retries because it
// lost the answer gets the same answer, and does not spend a second
// token doing it.
func (a *Authority) Enroll(req Request) (*Result, error) {
	csr, err := pki.DecodeCSR(req.CSR)
	if err != nil {
		return nil, err
	}
	nodeID, err := pki.NodeIDFromCSR(csr)
	if err != nil {
		return nil, err
	}
	fingerprint, err := pki.FingerprintCSR(csr)
	if err != nil {
		return nil, err
	}
	now := a.now()

	rec, err := a.Store.Get(nodeID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	if rec != nil {
		// A different key presenting an established name is the attack
		// this section exists to stop, and it is refused in every
		// state rather than treated as a new request.
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
		tok, spendErr := a.Store.SpendToken(req.Token, nodeID, req.RemoteAddr, now)
		if spendErr == nil {
			rec.Source = SourceToken
			rec.TokenID = tok.ID
			return a.issue(rec, csr, now)
		}
		// The request is still recorded as pending, so that a failed
		// automatic enrollment leaves something for an operator to
		// accept by hand rather than vanishing.
		if err := a.Store.Put(rec); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrRefused, spendErr)
	}

	if err := a.Store.Put(rec); err != nil {
		return nil, err
	}
	return &Result{NodeID: nodeID, State: Pending, Fingerprint: fingerprint}, ErrPending
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
func (a *Authority) issue(rec *Record, csr *x509.CertificateRequest, now time.Time) (*Result, error) {
	der, err := a.CA.IssueNode(csr, rec.NodeID, a.lifetime())
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("the certificate just issued for %s does not parse: %w", rec.NodeID, err)
	}
	// A reissue leaves the old serial valid until it expires, so it is
	// revoked here: a node holds one certificate at a time.
	if rec.Serial != "" && rec.Serial != pki.SerialString(cert) && a.Revoker != nil {
		a.Revoker.Revoke(rec.Serial, "superseded by a renewal")
	}
	rec.State = Accepted
	rec.Cert = string(pki.EncodeCert(der))
	rec.Serial = pki.SerialString(cert)
	rec.NotAfter = cert.NotAfter
	rec.Reason = ""
	rec.Updated = now
	if err := a.Store.Put(rec); err != nil {
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
	rec, err := a.Store.Get(nodeID)
	if err != nil {
		return nil, err
	}
	now := a.now()
	switch rec.Status(now) {
	case Accepted:
		return rec, nil
	case Revoked:
		return nil, fmt.Errorf("%s was revoked (%s); delete the record to let it enrol again", nodeID, rec.Reason)
	}
	if rec.CSR == "" {
		return nil, fmt.Errorf("%s has no certificate request on file to accept", nodeID)
	}
	csr, err := pki.DecodeCSR([]byte(rec.CSR))
	if err != nil {
		return nil, fmt.Errorf("the stored request for %s: %w", nodeID, err)
	}
	if _, err := a.issue(rec, csr, now); err != nil {
		return nil, err
	}
	return rec, nil
}

// Reject refuses a request and keeps it, for the audit.
func (a *Authority) Reject(nodeID, reason string) (*Record, error) {
	rec, err := a.Store.Get(nodeID)
	if err != nil {
		return nil, err
	}
	if rec.Status(a.now()) == Accepted {
		return nil, fmt.Errorf("%s is accepted; revoke it rather than rejecting it, so the serial reaches the denylist", nodeID)
	}
	rec.State = Rejected
	rec.Reason = reason
	rec.Updated = a.now()
	if err := a.Store.Put(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// Revoke withdraws an acceptance. The serial reaches the handshake
// denylist before the record is written, so the window in which a
// revoked certificate still works is not one that involves a disk.
func (a *Authority) Revoke(nodeID, reason string) (*Record, error) {
	rec, err := a.Store.Get(nodeID)
	if err != nil {
		return nil, err
	}
	if rec.Serial == "" {
		return nil, fmt.Errorf("%s holds no certificate to revoke", nodeID)
	}
	if a.Revoker != nil {
		a.Revoker.Revoke(rec.Serial, reason)
	}
	rec.State = Revoked
	rec.Reason = reason
	rec.Updated = a.now()
	if err := a.Store.Put(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// Renew reissues to a node that already holds a valid certificate,
// without an operator. SPEC 7.4: renewal needs no token and no
// decision, because the node has already authenticated as itself.
//
// The caller passes the certificate the peer authenticated with, not
// one from the request body.
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
	rec, err := a.Store.Get(nodeID)
	if err != nil {
		return nil, err
	}
	if rec.State != Accepted {
		return nil, fmt.Errorf("%w: %s is %s", ErrRefused, nodeID, rec.State)
	}
	if rec.Serial != pki.SerialString(peer) {
		return nil, fmt.Errorf("%w: %s presented serial %s and the hub issued %s",
			ErrRefused, nodeID, pki.SerialString(peer), rec.Serial)
	}
	fingerprint, err := pki.FingerprintCSR(csr)
	if err != nil {
		return nil, err
	}
	rec.Fingerprint = fingerprint
	rec.CSR = string(pki.EncodeCSR(csr.Raw))
	return a.issue(rec, csr, a.now())
}

// LoadDenylist puts every revoked serial on the handshake denylist. A
// hub that restarts and forgets what it revoked is a hub that lets a
// revoked node back in, so this runs before the listener opens.
func (a *Authority) LoadDenylist() (int, error) {
	if a.Revoker == nil {
		return 0, nil
	}
	records, err := a.Store.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range records {
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
