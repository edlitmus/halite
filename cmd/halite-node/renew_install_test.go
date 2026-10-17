package main

import (
	"os"
	"testing"

	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/renewal"
)

// **A renewal against a real hub installs through node.crt.new** (issue
// 321, M-26). The interrupted install is held in internal/renewal, by
// driving the files; this is the path a node takes, end to end: `renew
// --force` against a hub in this process, after which the node holds a
// new certificate that goes with its key, and nothing is left staged.
func TestARenewalInstallsAMatchingPairAndLeavesNothingStaged(t *testing.T) {
	l := newEnrollLab(t)
	if got := l.enroll(t); got.code != 2 {
		t.Fatalf("first enroll: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	if _, err := l.auth.Accept(l.nodeID); err != nil {
		t.Fatal(err)
	}
	if got := l.enroll(t); got.code != 0 {
		t.Fatalf("collecting the certificate: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	before, err := l.files.ReadCert(pki.NodeCertFile)
	if err != nil {
		t.Fatal(err)
	}
	oldPEM, err := os.ReadFile(l.files.Path(pki.NodeCertFile))
	if err != nil {
		t.Fatal(err)
	}

	if got := run(t, "renew", "--config", l.cfg, "--root", l.root, "--force"); got.code != 0 {
		t.Fatalf("renew --force: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	l.pairMatches(t, "after the renewal")
	after, err := l.files.ReadCert(pki.NodeCertFile)
	if err != nil {
		t.Fatal(err)
	}
	if after.SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Errorf("the certificate is still serial %s; nothing was installed", before.SerialNumber)
	}
	if l.files.Exists(renewal.NewCertFile) {
		t.Errorf("%s is still there after a finished renewal", renewal.NewCertFile)
	}

	// And an install interrupted after the key, made by hand: the new key,
	// the old certificate, which no longer goes with it, and the new one
	// staged. A plain `renew` loads the identity the way every command
	// does, through Recover, so it finishes the install rather than dying
	// on the pair; nothing is due, so it then does nothing more.
	newPEM, err := os.ReadFile(l.files.Path(pki.NodeCertFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.files.Path(renewal.NewCertFile), newPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.files.Path(pki.NodeCertFile), oldPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.files.KeyPair(pki.NodeCertFile, pki.NodeKeyFile); err == nil {
		t.Fatal("the interrupted state was not made: the pair still matches")
	}
	if got := run(t, "renew", "--config", l.cfg, "--root", l.root); got.code != 0 {
		t.Fatalf("renew after an interrupted install: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	l.pairMatches(t, "after recovering")
	if l.files.Exists(renewal.NewCertFile) {
		t.Errorf("%s is still there after it was recovered", renewal.NewCertFile)
	}
}
