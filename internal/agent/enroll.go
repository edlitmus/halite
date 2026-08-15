package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/transport"
)

// ensureEnrolled makes sure the agent holds a certificate, generating a key
// and asking for one if not. It waits, retrying, until an operator accepts:
// an agent that starts before its key is accepted should converge on its
// own once someone says yes, not exit and need restarting.
func (a *Agent) ensureEnrolled(ctx context.Context) error {
	switch expiry, err := a.certExpiry(); {
	case err != nil:
		// No certificate, or one that will not parse: enroll.
	case time.Now().Before(expiry):
		return nil
	default:
		// An expired certificate cannot renew, because the control plane
		// will not accept it on the wire. Enrolling again with the same
		// key is the way back: the request is already on file there, so a
		// control plane that recognises it reissues without an operator.
		a.log.Printf("this agent's certificate expired on %s; asking to be issued a new one for the same key",
			expiry.Format(time.RFC3339))
	}
	csrPEM, err := a.ensureKeyAndRequest()
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(a.cfg.agentKey())
	if err != nil {
		return err
	}
	fingerprint, err := ca.CSRFingerprint(csrPEM)
	if err != nil {
		return err
	}
	a.log.Printf("enrolling as %q with %s", a.cfg.ID, strings.Join(a.cfg.Masters, ", "))
	a.log.Printf("key fingerprint: %s", fingerprint)
	a.log.Printf("accept it on the control plane with: halite key accept %s", a.cfg.ID)

	// Enrollment runs before the agent has a certificate, so the client
	// authenticates the control plane but cannot yet authenticate itself.
	tlsCfg, err := transport.ClientTLS("", "", a.cfg.caCert())
	if err != nil {
		return err
	}
	request := transport.EnrollRequest{ID: a.cfg.ID, CSR: string(csrPEM)}

	for {
		// Try each control plane: the request has to be accepted by whichever
		// one is reachable, and they share a CA, so any of them will do.
		for _, addr := range a.cfg.Masters {
			client := transport.NewJSONClient(addr, tlsCfg, 30*time.Second)
			var resp transport.EnrollResponse
			err := client.Post(ctx, transport.PathEnroll, request, &resp)
			switch {
			case err != nil:
				a.log.Printf("enrollment via %s: %v", addr, err)
			case resp.State == string(ca.StateAccepted):
				// Checked before it is kept: a certificate that does not
				// verify, or that belongs to another key, would take the
				// agent down at its next start with nothing to retry with.
				// An expired one lands here too, from a control plane too
				// old to reissue.
				if err := a.verifyIssued([]byte(resp.Cert), keyPEM); err != nil {
					a.log.Printf("%s issued a certificate this agent cannot use: %v", addr, err)
					continue
				}
				if err := ca.ReplaceFile(a.cfg.agentCert(), []byte(resp.Cert), 0o644); err != nil {
					return fmt.Errorf("write certificate: %w", err)
				}
				a.log.Printf("enrolled: certificate written to %s", a.cfg.agentCert())
				return nil
			case resp.State == string(ca.StateRejected):
				return fmt.Errorf("enrollment rejected for %q; an operator must run 'halite key remove %s' before this host can retry",
					a.cfg.ID, a.cfg.ID)
			default:
				a.log.Printf("waiting to be accepted as %q on %s", a.cfg.ID, addr)
			}
		}
		if !sleepCtx(ctx, a.cfg.RetryInterval) {
			return ctx.Err()
		}
	}
}

// ensureKeyAndRequest returns this agent's signing request, generating the
// key on first run. The key is written 0600 and never leaves the host.
func (a *Agent) ensureKeyAndRequest() ([]byte, error) {
	if err := os.MkdirAll(a.cfg.PKIDir, 0o755); err != nil {
		return nil, err
	}
	csrPath := a.cfg.agentKey() + ".csr"
	keyPEM, keyErr := os.ReadFile(a.cfg.agentKey())
	if existing, err := os.ReadFile(csrPath); err == nil && keyErr == nil {
		return existing, nil
	}
	if keyErr == nil {
		// The key exists but its request sidecar is gone: rebuild the CSR
		// from the key rather than silently rolling a new identity — a
		// pending request on the control plane must keep matching the key
		// this host actually holds.
		if key, err := ca.ParseKey(keyPEM); err == nil {
			csrPEM, err := ca.NewCSR(key, a.cfg.ID, nil)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(csrPath, csrPEM, 0o644); err != nil {
				return nil, fmt.Errorf("write request: %w", err)
			}
			return csrPEM, nil
		}
		// An unreadable key is the one case where regenerating is right.
	}

	key, keyPEM, err := ca.GenerateKey()
	if err != nil {
		return nil, err
	}
	csrPEM, err := ca.NewCSR(key, a.cfg.ID, nil)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(a.cfg.agentKey(), keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	if err := os.WriteFile(csrPath, csrPEM, 0o644); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	return csrPEM, nil
}
