package master

import (
	"context"
	"crypto/x509"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/agent"
	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/transport"
)

// enrolledKeypair enrolls an identity and returns the client it can
// connect with, together with the key that certificate belongs to —
// renewal is about that key, so a test needs to hold it.
func (f *fleet) enrolledKeypair(t *testing.T, id string) (*transport.Client, []byte, []byte) {
	t.Helper()
	key, keyPEM, err := ca.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := ca.NewCSR(key, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp transport.EnrollResponse
	ctx := context.Background()
	if err := f.anonymousClient(t).Post(ctx, transport.PathEnroll,
		transport.EnrollRequest{ID: id, CSR: string(csrPEM)}, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Cert == "" {
		if _, err := f.store.Accept(id); err != nil {
			t.Fatal(err)
		}
		if err := f.anonymousClient(t).Post(ctx, transport.PathEnroll,
			transport.EnrollRequest{ID: id, CSR: string(csrPEM)}, &resp); err != nil {
			t.Fatal(err)
		}
	}
	return f.clientFrom(t, id, keyPEM, []byte(resp.Cert)), keyPEM, []byte(resp.Cert)
}

func TestAnAgentRenewsItsOwnCertificate(t *testing.T) {
	f := newFleet(t, Config{})
	client, keyPEM, certPEM := f.enrolledKeypair(t, "web1")

	key, err := ca.ParseKey(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := ca.NewCSR(key, "web1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp transport.RenewResponse
	if err := client.Post(context.Background(), transport.PathRenew,
		transport.RenewRequest{CSR: string(csrPEM)}, &resp); err != nil {
		t.Fatalf("renew: %v", err)
	}

	before, err := ca.ParseCert(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ca.ParseCert([]byte(resp.Cert))
	if err != nil {
		t.Fatal(err)
	}
	if after.SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Error("renewal returned the same certificate")
	}
	if after.Subject.CommonName != "web1" {
		t.Errorf("renewed certificate is for %q", after.Subject.CommonName)
	}
	// The identity is the certificate's, not the request's: a body cannot
	// name someone else, because there is nowhere in it to say so.
	if _, err := f.store.IssuedCert("web1"); err != nil {
		t.Fatal(err)
	}
}

func TestRenewalCannotChangeTheKey(t *testing.T) {
	f := newFleet(t, Config{})
	client, _, _ := f.enrolledKeypair(t, "web1")

	// A different key for the same identity is an enrollment, which an
	// operator decides. Renewal must not be a way around that.
	otherKey, _, err := ca.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := ca.NewCSR(otherKey, "web1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp transport.RenewResponse
	if err := client.Post(context.Background(), transport.PathRenew,
		transport.RenewRequest{CSR: string(csrPEM)}, &resp); err == nil {
		t.Fatal("renewing with a new key must be refused")
	}
}

func TestRenewalNeedsACertificateOfItsOwn(t *testing.T) {
	f := newFleet(t, Config{})

	// An operator certificate authenticates, but there is no accepted
	// agent entry behind it to renew.
	key, _, err := ca.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := ca.NewCSR(key, "admin1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp transport.RenewResponse
	if err := f.adminClient(t, "admin1").Post(context.Background(), transport.PathRenew,
		transport.RenewRequest{CSR: string(csrPEM)}, &resp); err == nil {
		t.Fatal("an identity with no accepted certificate must not renew")
	}

	// And no certificate at all is not a caller the route answers.
	if err := f.anonymousClient(t).Post(context.Background(), transport.PathRenew,
		transport.RenewRequest{CSR: string(csrPEM)}, &resp); err == nil {
		t.Fatal("renewal must not be reachable without a certificate")
	}
}

// TestAgentRenewsAgainstARunningControlPlane drives the agent's own
// renewal path against a real control plane over mTLS: the protocol tests
// above cover the endpoint, but only this one proves an agent notices,
// asks, writes the answer, and reconnects with it.
func TestAgentRenewsAgainstARunningControlPlane(t *testing.T) {
	f := newFleet(t, Config{AutoAccept: true})

	pki := t.TempDir()
	caPEM, err := f.store.CACertPEM()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pki, "ca.crt"), caPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	// RenewBefore longer than the certificate's life makes every
	// connection due for renewal, so the test does not wait a year.
	a, err := agent.New(agent.Config{
		ID:            "web1",
		Masters:       []string{f.host()},
		PKIDir:        pki,
		CacheDir:      t.TempDir(),
		RetryInterval: 20 * time.Millisecond,
		RenewBefore:   2 * ca.AgentCertLifetime,
	}, map[string]any{"host": "web1"}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := a.Run(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("agent: %v", err)
		}
	}()

	first := waitForCert(t, filepath.Join(pki, "agent.crt"), nil)
	renewed := waitForCert(t, filepath.Join(pki, "agent.crt"), first.SerialNumber.Bytes())
	cancel()
	<-done

	if renewed.Subject.CommonName != "web1" {
		t.Errorf("renewed certificate is for %q", renewed.Subject.CommonName)
	}
	// The control plane has to agree, or the agent renewed against nothing
	// and the next enrollment check would compare against a stale entry.
	storedPEM, err := f.store.IssuedCert("web1")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := ca.ParseCert(storedPEM)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SerialNumber.Cmp(renewed.SerialNumber) != 0 {
		t.Error("the control plane kept a different certificate than the agent holds")
	}
}

// waitForCert reads path until it holds a parseable certificate whose
// serial differs from notSerial, or the test times out.
func waitForCert(t *testing.T, path string, notSerial []byte) *x509.Certificate {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		certPEM, err := os.ReadFile(path)
		if err == nil {
			if cert, err := ca.ParseCert(certPEM); err == nil {
				if notSerial == nil || string(cert.SerialNumber.Bytes()) != string(notSerial) {
					return cert
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no new certificate at %s within the deadline", path)
	return nil
}
