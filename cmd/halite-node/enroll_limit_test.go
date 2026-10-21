package main

import (
	"context"
	"errors"
	"strings"
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
// waiting on an operator; the hub's one charge a second means the retry,
// five seconds on, is admitted.
func TestEnrollWaitRetriesWhenTheHubLimitsIt(t *testing.T) {
	l := newEnrollLab(t, func(s *hub.Server) {
		s.Authority.Mode = keystore.ModeToken
		s.EnrollSourceBurst = 1
		s.EnrollSourceRate = 1
	})
	_, secret, err := l.auth.Store.MintToken(keystore.TokenOptions{NodeGlob: l.nodeID, TTL: time.Hour}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	hubAddr := l.hub

	// spend fills the address's budget with a request that is charged: a
	// made-up token, refused.
	spend := func() {
		t.Helper()
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
	}

	// `--token=`, as the handbook says to write it: a minted token is
	// random base64 and begins with `-` about one time in 64, and
	// `--token <secret>` then reads it as a flag. Written with a space,
	// this test failed that often in CI.
	spend()
	got := l.enroll(t, "--token="+secret)
	if got.code != 2 || !strings.Contains(got.stdout, "limiting enrollment requests from this address") {
		t.Fatalf("enroll over the limit: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	if l.files.Exists(pki.NodeCertFile) {
		t.Fatal("a limited request left a certificate")
	}

	time.Sleep(1100 * time.Millisecond)
	spend()
	start := time.Now()
	got = l.enroll(t, "--token="+secret, "--wait")
	if got.code != 0 {
		t.Fatalf("enroll --wait over the limit: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "trying again in") {
		t.Errorf("--wait was never limited, so this proves nothing:\n%s", got.stdout)
	}
	if took := time.Since(start); took < 5*time.Second {
		t.Errorf("--wait retried after %s, sooner than its own interval", took)
	}
	l.pairMatches(t, "after --wait")
	if !l.files.Exists(pki.NodeCertFile) {
		t.Error("--wait finished without a certificate")
	}
}
