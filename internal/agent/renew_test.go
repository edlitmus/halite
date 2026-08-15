package agent

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/ca"
)

// enrolledAgent returns an agent whose PKI directory holds a CA, a key,
// and a certificate for that key — the state renewal starts from.
func enrolledAgent(t *testing.T, id string) (*Agent, *ca.Store, []byte) {
	t.Helper()
	dir := t.TempDir()
	store := &ca.Store{Dir: filepath.Join(dir, "ca")}
	if err := store.Init("test ca", time.Hour); err != nil {
		t.Fatal(err)
	}
	caPEM, err := store.CACertPEM()
	if err != nil {
		t.Fatal(err)
	}

	key, keyPEM, err := ca.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := ca.NewCSR(key, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Submit(id, csrPEM); err != nil {
		t.Fatal(err)
	}
	certPEM, err := store.Accept(id)
	if err != nil {
		t.Fatal(err)
	}

	pki := filepath.Join(dir, "pki")
	if err := os.MkdirAll(pki, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"ca.crt": caPEM, "agent.key": keyPEM, "agent.crt": certPEM,
	} {
		if err := os.WriteFile(filepath.Join(pki, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{ID: id, Masters: []string{"master.example.com"}, PKIDir: pki}
	cfg.withDefaults()
	return &Agent{cfg: cfg, log: log.New(io.Discard, "", 0)}, store, keyPEM
}

func TestCertExpiryReadsTheCertificateOnDisk(t *testing.T) {
	a, _, _ := enrolledAgent(t, "web1")
	expiry, err := a.certExpiry()
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(expiry); remaining < ca.AgentCertLifetime-time.Minute {
		t.Errorf("expiry in %s, want about %s", remaining, ca.AgentCertLifetime)
	}
}

// TestVerifyIssuedRefusesACertificateItCannotUse is the check that keeps a
// bad answer from replacing a working certificate. Writing one would take
// the agent down at its next start, with nothing left to retry with.
func TestVerifyIssuedRefusesACertificateItCannotUse(t *testing.T) {
	a, store, keyPEM := enrolledAgent(t, "web1")
	key, err := ca.ParseKey(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(a.cfg.agentCert())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.verifyIssued(good, keyPEM); err != nil {
		t.Fatalf("the certificate this agent actually holds must verify: %v", err)
	}

	// Right CA and right key, wrong name.
	otherName, err := ca.NewCSR(key, "web2", nil)
	if err != nil {
		t.Fatal(err)
	}
	misnamed, err := store.Sign(otherName, "web2", ca.RoleAgent, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.verifyIssued(misnamed, keyPEM); err == nil {
		t.Error("a certificate for another identity must be refused")
	}

	// Right name and right key, wrong CA.
	stranger := &ca.Store{Dir: filepath.Join(t.TempDir(), "ca")}
	if err := stranger.Init("someone else", time.Hour); err != nil {
		t.Fatal(err)
	}
	ourCSR, err := ca.NewCSR(key, "web1", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := stranger.Sign(ourCSR, "web1", ca.RoleAgent, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.verifyIssued(foreign, keyPEM); err == nil {
		t.Error("a certificate from another CA must be refused")
	}

	// Right name and right CA, wrong key.
	strangerKey, strangerKeyPEM, err := ca.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	strangerCSR, err := ca.NewCSR(strangerKey, "web1", nil)
	if err != nil {
		t.Fatal(err)
	}
	otherKeyCert, err := store.Sign(strangerCSR, "web1", ca.RoleAgent, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.verifyIssued(otherKeyCert, keyPEM); err == nil {
		t.Error("a certificate for another key must be refused")
	}
	if err := a.verifyIssued(otherKeyCert, strangerKeyPEM); err != nil {
		t.Errorf("that same certificate is fine for its own key: %v", err)
	}

	if err := a.verifyIssued([]byte("not a certificate"), keyPEM); err == nil {
		t.Error("a body that is not a certificate must be refused")
	}
}

// TestRenewalWaitsUntilTheCertificateIsNearlyUp keeps a fleet from asking
// for a new certificate on every poll for a year.
func TestRenewalWaitsUntilTheCertificateIsNearlyUp(t *testing.T) {
	a, _, _ := enrolledAgent(t, "web1")
	a.expiresAt = time.Now().Add(2 * a.cfg.RenewBefore)

	if a.renewIfDue(context.Background()) {
		t.Fatal("renewed a certificate that is nowhere near expiring")
	}
	if !a.nextRenewal.IsZero() {
		t.Error("a certificate that is not due must not consume the retry window")
	}

	// Due, but there is no connection to renew over: the attempt still
	// counts against the retry window, so a disconnected agent does not
	// spin.
	a.expiresAt = time.Now().Add(a.cfg.RenewBefore / 2)
	if a.renewIfDue(context.Background()) {
		t.Fatal("renewed without a control plane to renew with")
	}
	if a.nextRenewal.IsZero() {
		t.Error("a failed attempt must delay the next one")
	}
}
