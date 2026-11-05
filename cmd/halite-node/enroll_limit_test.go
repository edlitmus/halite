package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// **`enroll --wait` waits out the hub's per-source limit** (issue 321,
// M-06; DIVERGENCE 5.398). Every answer but "pending" used to end
// `--wait`, so a node behind an address the hub was limiting gave up at
// the first 429. Without --wait it says what happened and exits 2, as a
// pending answer does: nothing is wrong and nothing has finished.
//
// Token mode, so the retry that is admitted issues at once rather than
// waiting on an operator.
//
// **The hub's clock is the test's, not the wall's.** This test used to
// fill a budget of one, refilling at one a second, and then start the
// node, trusting the node to reach the hub inside that second. A `-race`
// build of this binary takes a second to print its version on a Mac, so
// the budget had refilled by the time it asked, the request was
// admitted, and the test failed eleven runs in fifteen there and about
// one in three in CI, at "exit 0" where it wanted 2. Nothing in the node
// or the hub was wrong: the node was told nothing, because there was
// nothing to tell it. The limiter's arithmetic against time is
// internal/hub's to hold, with its own clock (TestOneAddressIsHeldTo
// ItsEnrollmentBudget); what this test holds is what the node does with
// a 429, so the hub's clock stands still until the test has watched the
// node be limited, and moves only then.
func TestEnrollWaitRetriesWhenTheHubLimitsIt(t *testing.T) {
	// In the enroll handler only the limiter reads the server's clock
	// (server.go's admit and refund); the certificate is dated by the
	// Authority, which keeps its own and is not given this one. No node
	// subscribes here, so nothing else the server times is exercised.
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	l := newEnrollLab(t, func(s *hub.Server) {
		s.Authority.Mode = keystore.ModeToken
		s.EnrollSourceBurst = 1
		s.EnrollSourceRate = 1
		s.Now = func() time.Time { return time.Unix(0, clock.Load()) }
	})
	_, secret, err := l.auth.Store.MintToken(keystore.TokenOptions{NodeGlob: l.nodeID, TTL: time.Hour}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	hubAddr := l.hub

	// Fill the address's budget with a request that is charged: a
	// made-up token, refused. The clock does not move after this until
	// the test moves it, so the budget stays full however long the node
	// takes to start.
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := l.files.ReadCert(pki.CACertFile)
	if err != nil {
		t.Fatal(err)
	}
	client := &transport.Client{HubURL: "https://" + hubAddr, CA: ca, Timeout: 5 * time.Second}
	_, err = client.Enroll(context.Background(), key, "other.example", "not-a-token")
	var refused *transport.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("filling the budget: %v", err)
	}

	// `--token=`, as the handbook says to write it: a minted token is
	// random base64 and begins with `-` about one time in 64, and
	// `--token <secret>` then reads it as a flag. Written with a space,
	// this test failed that often in CI.
	got := l.enroll(t, "--token="+secret)
	if got.code != 2 || !strings.Contains(got.stdout, "limiting enrollment requests from this address") {
		t.Fatalf("enroll over the limit: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	if l.files.Exists(pki.NodeCertFile) {
		t.Fatal("a limited request left a certificate")
	}

	// A 429 is not charged, so the budget is still full: the first
	// attempt of --wait is limited too. The clock moves on only once the
	// node has said it was, and before the retry its own interval sets.
	limited := make(chan struct{})
	stdout := &watchFor{text: "trying again in", seen: limited}
	var stderr strings.Builder
	cmd := exec.Command(os.Args[0], "enroll", "--config", l.cfg, "--root", l.root, "--token="+secret, "--wait")
	cmd.Env = append(os.Environ(), reexec+"=1")
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-limited:
		clock.Add(int64(2 * time.Second))
	case err := <-exited:
		t.Fatalf("enroll --wait ended without waiting out the limit: %v\n%s%s", err, stdout, stderr.String())
	case <-time.After(60 * time.Second):
		t.Fatalf("enroll --wait never said it was limited\n%s%s", stdout, stderr.String())
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("enroll --wait over the limit: %v\n%s%s", err, stdout, stderr.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("enroll --wait was not admitted once the budget had drained\n%s%s", stdout, stderr.String())
	}
	if took := time.Since(start); took < 5*time.Second {
		t.Errorf("--wait retried after %s, sooner than its own interval", took)
	}
	l.pairMatches(t, "after --wait")
	if !l.files.Exists(pki.NodeCertFile) {
		t.Error("--wait finished without a certificate")
	}
}

// watchFor is a process's stdout that closes seen the first time text
// has been written to it, so a test can act on a line as it is printed
// rather than after the process has ended.
type watchFor struct {
	mu   sync.Mutex
	buf  strings.Builder
	text string
	seen chan struct{}
	done bool
}

func (w *watchFor) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if !w.done && strings.Contains(w.buf.String(), w.text) {
		w.done = true
		close(w.seen)
	}
	return len(p), nil
}

func (w *watchFor) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
