package keystore

import (
	"bufio"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// hubAndCLI is two Authorities over one directory with one CA, each
// with its own Store and its own denylist: the hub serving, and
// `halite-hub keys` run from an operator's shell. They share nothing
// but the files, so nothing in-process -- the Store's mutex, say -- can
// make the tests below pass while the cross-process race is open.
func hubAndCLI(t *testing.T) (hub, cli *Authority, hubRev, cliRev *fakeRevoker) {
	t.Helper()
	dir := t.TempDir()
	ca, err := pki.NewCA(pki.ECDSAP256, "test CA", 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mk := func() (*Authority, *fakeRevoker) {
		store, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		rev := &fakeRevoker{revoked: map[string]string{}}
		return &Authority{Store: store, CA: ca, Mode: ModeManual, Lifetime: DefaultLifetime, Revoker: rev}, rev
	}
	hub, hubRev = mk()
	cli, cliRev = mk()
	return hub, cli, hubRev, cliRev
}

// accepted enrols and accepts a node, and returns its certificate.
func accepted(t *testing.T, a *Authority, nodeID string) *x509.Certificate {
	t.Helper()
	csr, _ := request(t, nodeID)
	if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	rec, err := a.Accept(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := pki.DecodeCert([]byte(rec.Cert))
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// intrude runs other inside the window that store's critical section
// has open, the way the race needs it.
//
// When the lock works, other cannot have the store, so it is started
// and left to wait; the caller collects it with the returned function.
// When it does not -- which is the defect, and what a broken fix looks
// like -- other runs to completion inside the window, which is the
// interleaving that lost the revocation. Either way the outcome is
// fixed, so the test fails every time the exclusion is missing rather
// than whenever a scheduler happens to cooperate.
func intrude(t *testing.T, store *Store, other func()) (wait func()) {
	t.Helper()
	free, err := store.tryLocked()
	if err != nil {
		t.Fatalf("trying the lock: %v", err)
	}
	if free {
		t.Errorf("a second store took the lock while the first was between its read and its write")
		other()
		return func() {}
	}
	done := make(chan struct{})
	go func() { defer close(done); other() }()
	return func() {
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the waiting writer never got the lock")
		}
	}
}

// A revocation from the CLI that arrives while the hub is renewing the
// same node -- after the renewal has read the record and before it has
// written the new serial -- must end with the node revoked, and with the
// serial the renewal minted on the denylist. Before DIVERGENCE 5.290 the
// renewal's write landed last and left the node accepted with a serial
// nothing had denylisted.
func TestARevocationDuringARenewalRevokesTheNewSerial(t *testing.T) {
	hub, cli, _, cliRev := hubAndCLI(t)
	first := accepted(t, hub, "web1.example")

	var wait func()
	var revErr error
	hub.Store.between = func(op, id string) {
		if op != "renew" {
			return
		}
		hub.Store.between = nil
		wait = intrude(t, cli.Store, func() { _, revErr = cli.Revoke(id, "compromised") })
	}
	next, _ := request(t, "web1.example")
	res, err := hub.Renew(first, next)
	if wait == nil {
		t.Fatal("the window was never entered, so this test proved nothing")
	}
	wait()
	if err != nil {
		t.Fatalf("the renewal that held the store was refused: %v", err)
	}
	if revErr != nil {
		t.Fatalf("the revocation failed: %v", revErr)
	}
	renewed, err := pki.DecodeCert(res.Cert)
	if err != nil {
		t.Fatal(err)
	}

	rec, err := hub.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != Revoked {
		t.Fatalf("the node is %s after a revocation: the renewal undid it", rec.State)
	}
	if rec.Serial != pki.SerialString(renewed) {
		t.Errorf("the revoked record holds serial %s; the node holds %s", rec.Serial, pki.SerialString(renewed))
	}
	if _, ok := cliRev.revoked[pki.SerialString(renewed)]; !ok {
		t.Error("the serial the renewal issued was never denylisted")
	}
	// And what a hub restarting reads back off the disk denylists it.
	fresh := &fakeRevoker{revoked: map[string]string{}}
	hub.Revoker = fresh
	if _, err := hub.LoadDenylist(); err != nil {
		t.Fatal(err)
	}
	if _, ok := fresh.revoked[pki.SerialString(renewed)]; !ok {
		t.Error("a restarted hub does not denylist the renewed serial")
	}
}

// The other order: the revocation has read the record and the hub's
// renewal arrives before it writes. The renewal must be refused -- not
// issue a certificate that the revocation, holding the old serial, will
// never denylist.
func TestARenewalDuringARevocationIsRefused(t *testing.T) {
	hub, cli, _, cliRev := hubAndCLI(t)
	first := accepted(t, hub, "web1.example")

	var wait func()
	var res *Result
	var renewErr error
	next, _ := request(t, "web1.example")
	cli.Store.between = func(op, id string) {
		if op != "revoke" {
			return
		}
		cli.Store.between = nil
		wait = intrude(t, hub.Store, func() { res, renewErr = hub.Renew(first, next) })
	}
	if _, err := cli.Revoke("web1.example", "compromised"); err != nil {
		t.Fatal(err)
	}
	if wait == nil {
		t.Fatal("the window was never entered, so this test proved nothing")
	}
	wait()
	if !errors.Is(renewErr, ErrRefused) {
		got := "no certificate"
		if res != nil {
			if c, err := pki.DecodeCert(res.Cert); err == nil {
				got = "serial " + pki.SerialString(c)
			}
		}
		t.Fatalf("a renewal racing a revocation returned %v and %s", renewErr, got)
	}
	rec, err := hub.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != Revoked || rec.Serial != pki.SerialString(first) {
		t.Errorf("the record is %s with serial %s, want revoked with %s", rec.State, rec.Serial, pki.SerialString(first))
	}
	if _, ok := cliRev.revoked[pki.SerialString(first)]; !ok {
		t.Error("the revoked serial was not denylisted")
	}
}

// Revoked is sticky. Nothing an operator or a node does short of
// deleting the record takes a node out of it, because LoadDenylist puts
// only revoked serials back on the denylist at a restart.
func TestRevokedIsSticky(t *testing.T) {
	a, _ := newAuthority(t)
	cert := accepted(t, a, "web1.example")
	if _, err := a.Revoke("web1.example", "compromised"); err != nil {
		t.Fatal(err)
	}
	next, _ := request(t, "web1.example")
	if _, err := a.Renew(cert, next); !errors.Is(err, ErrRefused) {
		t.Errorf("a revoked node renewed: %v", err)
	}
	if _, err := a.Accept("web1.example"); err == nil {
		t.Error("a revoked node was accepted again")
	}
	if _, err := a.Reject("web1.example", "changed my mind"); err == nil {
		t.Error("a revoked node was rejected, which drops its serial from the denylist at the next restart")
	}
	// issue is the last place a serial is made, so it refuses on its
	// own account too, whatever a caller checked.
	rec, err := a.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := pki.DecodeCSR([]byte(rec.CSR))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.locked(func() error { _, err := a.issue(rec, csr, a.now()); return err }); !errors.Is(err, ErrRefused) {
		t.Errorf("issue minted a certificate for a revoked record: %v", err)
	}
	rec, err = a.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != Revoked {
		t.Errorf("the record is %s", rec.State)
	}
}

// A bootstrap token revoked from the CLI while the hub is spending it
// stays revoked. Before DIVERGENCE 5.290 the spend wrote back the whole
// token record, read before the revocation, and the token went on
// admitting nodes.
func TestATokenRevocationDuringASpendIsNotUndone(t *testing.T) {
	hub, cli, _, _ := hubAndCLI(t)
	hub.Mode = ModeToken
	now := time.Now()
	hub.Now = func() time.Time { return now }
	tok, secret, err := hub.Store.MintToken(TokenOptions{TTL: time.Hour, MaxUses: 5}, now)
	if err != nil {
		t.Fatal(err)
	}

	var wait func()
	var revErr error
	hub.Store.between = func(op, id string) {
		if op != "spend" {
			return
		}
		hub.Store.between = nil
		wait = intrude(t, cli.Store, func() { revErr = cli.Store.RevokeToken(id) })
	}
	csr, _ := request(t, "web1.example")
	if _, err := hub.Enroll(Request{CSR: csr, Token: secret, RemoteAddr: "10.0.0.1:1"}); err != nil {
		t.Fatalf("the enrollment already under way: %v", err)
	}
	if wait == nil {
		t.Fatal("the window was never entered, so this test proved nothing")
	}
	wait()
	if revErr != nil {
		t.Fatal(revErr)
	}
	back, err := hub.Store.GetToken(tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Revoked {
		t.Fatal("the spend wrote the token back unrevoked")
	}
	other, _ := request(t, "web2.example")
	if _, err := hub.Enroll(Request{CSR: other, Token: secret, RemoteAddr: "10.0.0.1:1"}); !errors.Is(err, ErrRefused) {
		t.Errorf("a revoked token admitted another node: %v", err)
	}
}

// The lock is an OS lock and excludes a separate process, not only a
// second Store in this one. The test binary runs itself as the other
// process, which takes the store's lock and holds it until told to let
// go.
func TestTheStoreLockExcludesAnotherProcess(t *testing.T) {
	if dir := os.Getenv("HALITE_KEYSTORE_LOCK_HOLDER"); dir != "" {
		holdTheLock(dir)
		return
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestTheStoreLockExcludesAnotherProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), "HALITE_KEYSTORE_LOCK_HOLDER="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	held := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "held" {
				held <- nil
				io.Copy(io.Discard, stdout)
				return
			}
		}
		held <- errors.New("the other process exited without taking the lock")
	}()
	select {
	case err := <-held:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the other process never took the lock")
	}

	free, err := s.tryLocked()
	if err != nil {
		t.Fatal(err)
	}
	if free {
		t.Fatal("this process took the store's lock while another process held it")
	}

	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the other process: %v", err)
	}
	free, err = s.tryLocked()
	if err != nil {
		t.Fatal(err)
	}
	if !free {
		t.Fatal("the lock was not released when the other process let go")
	}
}

func holdTheLock(dir string) {
	s, err := Open(dir)
	if err != nil {
		os.Exit(2)
	}
	err = s.locked(func() error {
		os.Stdout.WriteString("held\n")
		io.Copy(io.Discard, os.Stdin)
		return nil
	})
	if err != nil {
		os.Exit(3)
	}
}
