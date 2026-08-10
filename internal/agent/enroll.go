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
	if _, err := os.Stat(a.cfg.agentCert()); err == nil {
		return nil
	}
	csrPEM, err := a.ensureKeyAndRequest()
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
				if err := os.WriteFile(a.cfg.agentCert(), []byte(resp.Cert), 0o644); err != nil {
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
	if existing, err := os.ReadFile(csrPath); err == nil {
		if _, err := os.Stat(a.cfg.agentKey()); err == nil {
			return existing, nil
		}
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
