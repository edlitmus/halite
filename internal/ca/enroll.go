package ca

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AgentCertLifetime is how long an issued agent certificate is valid.
// Re-enrollment is cheap, so this is deliberately shorter than the CA.
const AgentCertLifetime = 365 * 24 * time.Hour

// State is where an identity sits in the enrollment lifecycle.
type State string

const (
	StatePending  State = "pending"
	StateAccepted State = "accepted"
	StateRejected State = "rejected"
)

// Entry is one identity known to the CA.
type Entry struct {
	ID          string
	State       State
	Fingerprint string
	Since       time.Time
}

// Submit records a CSR from an agent as pending. Re-submitting the same
// request is a no-op so an agent can retry safely; submitting a *different*
// key for an already-known identity is refused, because that is either a
// misconfigured host or an impersonation attempt.
func (s *Store) Submit(id string, csrPEM []byte) (State, error) {
	if err := ValidateID(id); err != nil {
		return "", err
	}
	csr, err := ParseCSR(csrPEM)
	if err != nil {
		return "", err
	}
	if csr.Subject.CommonName != id {
		return "", fmt.Errorf("csr common name %q does not match id %q", csr.Subject.CommonName, id)
	}

	if _, err := os.Stat(s.path("accepted", id+".crt")); err == nil {
		return StateAccepted, nil
	}
	for _, known := range []struct {
		dir   string
		state State
	}{{"pending", StatePending}, {"rejected", StateRejected}} {
		path := s.path(known.dir, id+".csr")
		existing, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(csrPEM)) {
			return "", fmt.Errorf("id %q already has a different key %s; remove it first", id, known.state)
		}
		return known.state, nil
	}
	if err := writeNew(s.path("pending", id+".csr"), csrPEM, 0o644); err != nil {
		return "", fmt.Errorf("store request: %w", err)
	}
	return StatePending, nil
}

// Accept signs a pending request and moves it to accepted, returning the
// issued certificate. Accepting an already-accepted id returns the existing
// certificate rather than issuing a second one.
func (s *Store) Accept(id string) ([]byte, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	if certPEM, err := os.ReadFile(s.path("accepted", id+".crt")); err == nil {
		return certPEM, nil
	}
	csrPath := s.path("pending", id+".csr")
	csrPEM, err := os.ReadFile(csrPath)
	if err != nil {
		return nil, fmt.Errorf("no pending request for %q", id)
	}
	certPEM, err := s.Sign(csrPEM, id, RoleAgent, nil, AgentCertLifetime)
	if err != nil {
		return nil, err
	}
	if err := writeNew(s.path("accepted", id+".crt"), certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("store certificate: %w", err)
	}
	// The CSR is kept next to the certificate so a later re-submission can
	// be recognised as the same key.
	if err := os.Rename(csrPath, s.path("accepted", id+".csr")); err != nil {
		return nil, fmt.Errorf("archive request: %w", err)
	}
	return certPEM, nil
}

// Reject moves a pending request to rejected. The agent keeps retrying and
// keeps being refused until an operator removes the entry.
func (s *Store) Reject(id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	if _, err := os.Stat(s.path("pending", id+".csr")); err != nil {
		return fmt.Errorf("no pending request for %q", id)
	}
	if err := os.MkdirAll(s.path("rejected"), 0o755); err != nil {
		return err
	}
	return os.Rename(s.path("pending", id+".csr"), s.path("rejected", id+".csr"))
}

// Remove forgets an identity entirely, in any state. The next request from
// that host starts over as pending.
func (s *Store) Remove(id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	found := false
	for _, rel := range []string{
		filepath.Join("pending", id+".csr"),
		filepath.Join("accepted", id+".crt"),
		filepath.Join("accepted", id+".csr"),
		filepath.Join("rejected", id+".csr"),
	} {
		if err := os.Remove(s.path(rel)); err == nil {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("unknown id %q", id)
	}
	return nil
}

// IssuedCert returns the certificate for an accepted identity, or an error
// if it is not accepted.
func (s *Store) IssuedCert(id string) ([]byte, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(s.path("accepted", id+".crt"))
	if err != nil {
		return nil, fmt.Errorf("id %q is not accepted", id)
	}
	return certPEM, nil
}

// List returns every known identity, sorted by state then id.
func (s *Store) List() ([]Entry, error) {
	var out []Entry
	for _, d := range []struct {
		dir   string
		ext   string
		state State
	}{
		{"pending", ".csr", StatePending},
		{"accepted", ".crt", StateAccepted},
		{"rejected", ".csr", StateRejected},
	} {
		entries, err := os.ReadDir(s.path(d.dir))
		if err != nil {
			continue // an absent directory just means nothing in that state
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), d.ext) {
				continue
			}
			id := strings.TrimSuffix(e.Name(), d.ext)
			entry := Entry{ID: id, State: d.state}
			if info, err := e.Info(); err == nil {
				entry.Since = info.ModTime()
			}
			entry.Fingerprint = s.fingerprintOf(d.dir, id, d.state)
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].State != out[j].State {
			return out[i].State < out[j].State
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// fingerprintOf reports the public-key fingerprint for a listed identity,
// or an empty string if it cannot be read — listing must not fail because
// one file is malformed.
func (s *Store) fingerprintOf(dir, id string, state State) string {
	if state == StateAccepted {
		certPEM, err := os.ReadFile(s.path(dir, id+".crt"))
		if err != nil {
			return ""
		}
		cert, err := parseCertPEM(certPEM)
		if err != nil {
			return ""
		}
		return Fingerprint(cert.RawSubjectPublicKeyInfo)
	}
	csrPEM, err := os.ReadFile(s.path(dir, id+".csr"))
	if err != nil {
		return ""
	}
	fp, err := CSRFingerprint(csrPEM)
	if err != nil {
		return ""
	}
	return fp
}
