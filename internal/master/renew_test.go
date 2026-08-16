package master

import (
	"context"
	"crypto"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/agent"
	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/logging"
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
	}, map[string]any{"host": "web1"}, logging.Discard())
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

// expireIssued replaces the certificate an identity holds, on the control
// plane and wherever else the test keeps a copy, with one that has already
// run out — the state a host switched off for a year comes back to.
func expireIssued(t *testing.T, f *fleet, id string, csrPEM []byte, copies ...string) []byte {
	t.Helper()
	stale, err := f.store.Sign(csrPEM, id, ca.RoleAgent, nil, -30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	paths := append([]string{filepath.Join(f.pki, "accepted", id+".crt")}, copies...)
	for _, path := range paths {
		if err := os.WriteFile(path, stale, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return stale
}

// TestAnExpiredIdentityEnrollsItsWayBack covers the host that was off for
// longer than a certificate lasts. It cannot renew — the control plane
// will not accept an expired certificate on the wire — so the only route
// left is the one it enrolled through.
func TestAnExpiredIdentityEnrollsItsWayBack(t *testing.T) {
	f := newFleet(t, Config{})
	_, keyPEM, _ := f.enrolledKeypair(t, "web1")
	csrPEM, err := os.ReadFile(filepath.Join(f.pki, "accepted", "web1.csr"))
	if err != nil {
		t.Fatal(err)
	}
	stale := expireIssued(t, f, "web1", csrPEM)

	var resp transport.EnrollResponse
	if err := f.anonymousClient(t).Post(context.Background(), transport.PathEnroll,
		transport.EnrollRequest{ID: "web1", CSR: string(csrPEM)}, &resp); err != nil {
		t.Fatalf("re-enroll: %v", err)
	}
	if resp.Cert == "" || resp.Cert == string(stale) {
		t.Fatal("the control plane handed back the expired certificate")
	}
	fresh, err := ca.ParseCert([]byte(resp.Cert))
	if err != nil {
		t.Fatal(err)
	}
	if !time.Now().Before(fresh.NotAfter) {
		t.Fatal("the reissued certificate is not valid now")
	}

	// The proof is that it works: connect with it.
	client := f.clientFrom(t, "web1", keyPEM, []byte(resp.Cert))
	if err := client.Post(context.Background(), transport.PathHello,
		transport.HelloRequest{Grains: map[string]any{"id": "web1"}}, nil); err != nil {
		t.Fatalf("the reissued certificate does not work: %v", err)
	}

	// And an attacker who noticed the expiry still cannot take the id: the
	// reissue is for the request on file, not for anything they send.
	other, err := ca.NewCSR(mustKey(t), "web1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.anonymousClient(t).Post(context.Background(), transport.PathEnroll,
		transport.EnrollRequest{ID: "web1", CSR: string(other)}, &resp); err == nil {
		t.Fatal("a different key for an expired identity must still be refused")
	}
}

func mustKey(t *testing.T) crypto.Signer {
	t.Helper()
	key, _, err := ca.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestAgentRecoversFromAnExpiredCertificate drives the whole recovery in
// real processes' worth of code: an agent that enrolled, was off past its
// expiry, and comes back to a control plane that has never stopped.
func TestAgentRecoversFromAnExpiredCertificate(t *testing.T) {
	f := newFleet(t, Config{AutoAccept: true})
	pki := t.TempDir()
	caPEM, err := f.store.CACertPEM()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pki, "ca.crt"), caPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	run := func() {
		t.Helper()
		a, err := agent.New(agent.Config{
			ID:            "web1",
			Masters:       []string{f.host()},
			PKIDir:        pki,
			CacheDir:      t.TempDir(),
			RetryInterval: 20 * time.Millisecond,
		}, map[string]any{"host": "web1"}, logging.Discard())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := a.Run(ctx); err != nil && ctx.Err() == nil {
				t.Errorf("agent: %v", err)
			}
		}()
		waitForCert(t, filepath.Join(pki, "agent.crt"), expiredSerial(t, filepath.Join(pki, "agent.crt")))
		cancel()
		<-done
	}

	run() // enrolls
	csrPEM, err := os.ReadFile(filepath.Join(pki, "agent.key.csr"))
	if err != nil {
		t.Fatal(err)
	}
	stale := expireIssued(t, f, "web1", csrPEM, filepath.Join(pki, "agent.crt"))
	staleCert, err := ca.ParseCert(stale)
	if err != nil {
		t.Fatal(err)
	}

	run() // comes back with an expired certificate and recovers

	recovered := waitForCert(t, filepath.Join(pki, "agent.crt"), staleCert.SerialNumber.Bytes())
	if !time.Now().Before(recovered.NotAfter) {
		t.Fatal("the agent kept an expired certificate")
	}
	if recovered.Subject.CommonName != "web1" {
		t.Errorf("recovered certificate is for %q", recovered.Subject.CommonName)
	}
}

// expiredSerial returns the serial of whatever certificate is at path, or
// nil if there is none yet — so waitForCert means "any certificate" on the
// first run and "a different one" afterwards.
func expiredSerial(t *testing.T, path string) []byte {
	t.Helper()
	certPEM, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	cert, err := ca.ParseCert(certPEM)
	if err != nil {
		return nil
	}
	if time.Now().Before(cert.NotAfter) {
		return nil // already usable; nothing to wait for
	}
	return cert.SerialNumber.Bytes()
}

// TestARevokedAgentIsRefusedEverywhere is the enforcement half of
// revocation: the certificate stays cryptographically valid, so the
// control plane is what has to say no — on every route, without a
// restart.
func TestARevokedAgentIsRefusedEverywhere(t *testing.T) {
	f := newFleet(t, Config{})
	client, keyPEM, _ := f.enrolledKeypair(t, "web1")
	ctx := context.Background()

	hello := transport.HelloRequest{Grains: map[string]any{"id": "web1"}}
	if err := client.Post(ctx, transport.PathHello, hello, nil); err != nil {
		t.Fatalf("the agent should work before it is revoked: %v", err)
	}

	// Revoked while the control plane is running and holding this
	// connection open.
	events, unsubscribe := f.server.Bus().Subscribe("halite/key/web1/refused")
	defer unsubscribe()
	if err := f.store.Revoke("web1"); err != nil {
		t.Fatal(err)
	}

	// Each of these has to be refused *for being revoked* — a poll that
	// merely timed out, or a renewal that failed because the certificate
	// moved, would pass a bare "did it error" check while the denylist
	// did nothing.
	refused := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("a revoked agent must not %s", what)
			return
		}
		if !strings.Contains(err.Error(), "revoked") {
			t.Errorf("%s was refused for the wrong reason: %v", what, err)
		}
	}

	refused("say hello", client.Post(ctx, transport.PathHello, hello, nil))
	var jobs []transport.Job
	refused("poll for work", client.Get(ctx, transport.PathJobs, &jobs))
	var pillarData map[string]any
	refused("fetch pillar", client.Get(ctx, transport.PathPillar, &pillarData))

	key, err := ca.ParseKey(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := ca.NewCSR(key, "web1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var renewed transport.RenewResponse
	refused("renew", client.Post(ctx, transport.PathRenew,
		transport.RenewRequest{CSR: string(csrPEM)}, &renewed))

	// An operator has to be able to see that a revoked host is still out
	// there knocking.
	select {
	case ev := <-events:
		if ev.Data["id"] != "web1" {
			t.Errorf("refusal event carries %v", ev.Data)
		}
	case <-time.After(5 * time.Second):
		t.Error("no halite/key/web1/refused event was raised")
	}

	// Nor can it go back to the door it came in through.
	var resp transport.EnrollResponse
	if err := f.anonymousClient(t).Post(ctx, transport.PathEnroll,
		transport.EnrollRequest{ID: "web1", CSR: string(csrPEM)}, &resp); err == nil {
		t.Errorf("a revoked identity must not enroll again: %+v", resp)
	}
}

// TestRevokingOneAgentLeavesTheRestAlone guards the obvious way to get
// this wrong.
func TestRevokingOneAgentLeavesTheRestAlone(t *testing.T) {
	f := newFleet(t, Config{})
	web1, _, _ := f.enrolledKeypair(t, "web1")
	web2, _, _ := f.enrolledKeypair(t, "web2")
	ctx := context.Background()

	if err := f.store.Revoke("web1"); err != nil {
		t.Fatal(err)
	}
	if err := web1.Post(ctx, transport.PathHello,
		transport.HelloRequest{Grains: map[string]any{"id": "web1"}}, nil); err == nil {
		t.Error("web1 was revoked")
	}
	if err := web2.Post(ctx, transport.PathHello,
		transport.HelloRequest{Grains: map[string]any{"id": "web2"}}, nil); err != nil {
		t.Errorf("web2 was not: %v", err)
	}
}
