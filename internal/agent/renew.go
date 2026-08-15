package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/transport"
)

// renewRetryInterval paces another attempt after a failed renewal. The
// certificate is still valid for a while when renewal starts, so there is
// no reason to ask again every poll — and an agent that hammers a control
// plane it cannot renew with is the last thing an operator needs.
const renewRetryInterval = time.Hour

// certExpiry reads when this agent's certificate stops being valid. A
// certificate that cannot be read has no expiry to act on: the connection
// will fail on its own and say why.
func (a *Agent) certExpiry() (time.Time, error) {
	certPEM, err := os.ReadFile(a.cfg.agentCert())
	if err != nil {
		return time.Time{}, err
	}
	cert, err := ca.ParseCert(certPEM)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}

// renewIfDue replaces the certificate when it is close to expiring,
// reporting whether it did. A true return means the caller has to
// reconnect: the connection it is on was made with the old one.
//
// Renewal keeps the key. The control plane refuses a request for any
// other key, because changing keys is an enrollment an operator decides.
func (a *Agent) renewIfDue(ctx context.Context) bool {
	if a.expiresAt.IsZero() || time.Until(a.expiresAt) > a.cfg.RenewBefore {
		return false
	}
	if time.Now().Before(a.nextRenewal) {
		return false
	}
	// Set before the attempt, and left alone after a successful one: at
	// most one renewal an hour, however the last one went. A misconfigured
	// RenewBefore longer than the certificate's life would otherwise mean
	// a renewal on every poll, forever.
	a.nextRenewal = time.Now().Add(renewRetryInterval)

	client := a.currentClient()
	if client == nil {
		return false
	}
	certPEM, err := a.requestRenewal(ctx, client)
	if err != nil {
		// The current certificate is still valid — that is the whole point
		// of starting well before it expires — so this is worth a line in
		// the log and another attempt later, not an exit.
		a.log.Printf("renewing the certificate: %v (expires %s)",
			err, a.expiresAt.Format(time.RFC3339))
		return false
	}
	if err := ca.ReplaceFile(a.cfg.agentCert(), certPEM, 0o644); err != nil {
		a.log.Printf("writing the renewed certificate: %v", err)
		return false
	}
	cert, err := ca.ParseCert(certPEM)
	if err != nil {
		return false
	}
	a.expiresAt = cert.NotAfter
	a.log.Printf("certificate renewed; valid until %s", cert.NotAfter.Format(time.RFC3339))
	return true
}

// requestRenewal builds a request for the key this agent already holds,
// asks the control plane to sign it, and checks the answer before it is
// allowed anywhere near the PKI directory.
func (a *Agent) requestRenewal(ctx context.Context, client *transport.Client) ([]byte, error) {
	keyPEM, err := os.ReadFile(a.cfg.agentKey())
	if err != nil {
		return nil, err
	}
	key, err := ca.ParseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	csrPEM, err := ca.NewCSR(key, a.cfg.ID, nil)
	if err != nil {
		return nil, err
	}

	renewCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var resp transport.RenewResponse
	if err := client.Post(renewCtx, transport.PathRenew, transport.RenewRequest{CSR: string(csrPEM)}, &resp); err != nil {
		return nil, err
	}
	if err := a.verifyIssued([]byte(resp.Cert), keyPEM); err != nil {
		return nil, err
	}
	return []byte(resp.Cert), nil
}

// verifyIssued checks a certificate before it replaces the working one: it
// has to parse, to chain to the CA this agent trusts, and to belong to the
// key on disk. Writing an answer that does not satisfy all three would
// take the agent down at its next start, when nothing is left to retry
// with.
func (a *Agent) verifyIssued(certPEM, keyPEM []byte) error {
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return fmt.Errorf("the issued certificate does not match this agent's key: %w", err)
	}
	caPEM, err := os.ReadFile(a.cfg.caCert())
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("no CA certificate in %s", a.cfg.caCert())
	}
	cert, err := ca.ParseCert(certPEM)
	if err != nil {
		return err
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return fmt.Errorf("the issued certificate does not verify against %s: %w", a.cfg.caCert(), err)
	}
	if cert.Subject.CommonName != a.cfg.ID {
		return fmt.Errorf("the issued certificate is for %q, not %q", cert.Subject.CommonName, a.cfg.ID)
	}
	return nil
}
