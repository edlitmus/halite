package main

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// enrollLab is a real hub in manual mode and a node configuration that
// points at it, with the hub's CA already pinned in the node's pki_dir.
type enrollLab struct {
	auth   *keystore.Authority
	cfg    string
	root   string
	files  pki.Files
	nodeID string
	// hub is the address the hub listens on.
	hub string
	// keys is the hub's key store directory.
	keys string
}

func newEnrollLab(t *testing.T, configure ...func(*hub.Server)) *enrollLab {
	t.Helper()
	ca, err := pki.NewCA(pki.ECDSAP256, "enroll test CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keys := t.TempDir()
	store, err := keystore.Open(keys)
	if err != nil {
		t.Fatal(err)
	}
	denied := transport.NewDenylist()
	auth := &keystore.Authority{Store: store, CA: ca, Mode: keystore.ModeManual, Revoker: denied, Lifetime: time.Hour}
	hubKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	hubDER, err := ca.IssueHub(hubKey, []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := hub.Listen("127.0.0.1:0", hub.NewServingCert(tls.Certificate{Certificate: [][]byte{hubDER}, PrivateKey: hubKey}),
		ca.Cert, denied)
	if err != nil {
		t.Fatal(err)
	}
	srv := &hub.Server{Authority: auth, Fleet: hub.NewFleet(), PingInterval: time.Second}
	for _, f := range configure {
		f(srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); ln.Close(); <-done })

	dir := t.TempDir()
	files := pki.Files{Dir: filepath.Join(dir, "pki")}
	if err := os.MkdirAll(files.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCert(pki.CACertFile, ca.Cert.Raw); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "node.yaml")
	body := "node_id: web1.example\nhub: " + ln.Addr().String() + "\npki_dir: " + files.Dir + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return &enrollLab{auth: auth, cfg: cfg, root: t.TempDir(), files: files, nodeID: "web1.example", hub: ln.Addr().String(), keys: keys}
}

func (l *enrollLab) enroll(t *testing.T, extra ...string) result {
	t.Helper()
	return run(t, append([]string{"enroll", "--config", l.cfg, "--root", l.root}, extra...)...)
}

// pairMatches is what every later enroll and connect checks first.
func (l *enrollLab) pairMatches(t *testing.T, when string) {
	t.Helper()
	if !l.files.Exists(pki.NodeCertFile) {
		return
	}
	if _, err := l.files.KeyPair(pki.NodeCertFile, pki.NodeKeyFile); err != nil {
		t.Fatalf("%s: the node's certificate and key no longer go together: %v", when, err)
	}
}

func (l *enrollLab) pendingFingerprint(t *testing.T) string {
	t.Helper()
	rec, err := l.auth.Store.Get(l.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Fingerprint
}

// **`enroll --force` keeps the node's working identity until the hub
// issues** (issue 321, M-25). It wrote the new key over the old before
// the hub answered, while node.crt stayed: a pending answer left a pair
// that did not match, and every later `enroll`, with --force or without,
// died on it. Now the new key waits as node.key.pending, a plain `enroll`
// continues the request with it, and it is installed when the
// certificate comes.
func TestEnrollForceKeepsAWorkingIdentityUntilIssued(t *testing.T) {
	l := newEnrollLab(t)

	// A first enrollment, accepted.
	if got := l.enroll(t); got.code != 2 {
		t.Fatalf("first enroll: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	if _, err := l.auth.Accept(l.nodeID); err != nil {
		t.Fatal(err)
	}
	if got := l.enroll(t); got.code != 0 {
		t.Fatalf("collecting the certificate: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	first, err := l.files.ReadKey(pki.NodeKeyFile)
	if err != nil {
		t.Fatal(err)
	}

	// The node is rebuilt: the operator deletes its record, and the node
	// starts again from a new key.
	if err := l.auth.Store.Delete(l.nodeID); err != nil {
		t.Fatal(err)
	}
	if got := l.enroll(t, "--force"); got.code != 2 {
		t.Fatalf("enroll --force: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	l.pairMatches(t, "after a pending --force")
	if !l.files.Exists(pki.NodeKeyFile + ".pending") {
		t.Fatal("the new key was not kept as pending")
	}
	held := l.pendingFingerprint(t)

	// A plain enroll, which is what the message says to run, continues
	// the same request rather than dying or starting another.
	if got := l.enroll(t); got.code != 2 {
		t.Fatalf("enroll after a pending --force: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	if fp := l.pendingFingerprint(t); fp != held {
		t.Errorf("the second enroll asked with a different key: %s, the hub holds %s", fp, held)
	}
	l.pairMatches(t, "after continuing")

	if _, err := l.auth.Accept(l.nodeID); err != nil {
		t.Fatal(err)
	}
	if got := l.enroll(t); got.code != 0 {
		t.Fatalf("collecting the new certificate: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	l.pairMatches(t, "after the new certificate")
	if l.files.Exists(pki.NodeKeyFile + ".pending") {
		t.Error("the pending key is still there after it was installed")
	}
	now, err := l.files.ReadKey(pki.NodeKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	fpNow, _ := pki.FingerprintKey(now.Public())
	fpFirst, _ := pki.FingerprintKey(first.Public())
	if fpNow != held || fpNow == fpFirst {
		t.Errorf("the installed key is %s; the hub issued for %s and the first key was %s", fpNow, held, fpFirst)
	}
}

// **A refused --force keeps the identity and drops the new key.** The
// commonest trigger the triage found: --force on a node the hub has
// accepted, which the hub refuses as "a different key".
func TestARefusedEnrollForceKeepsTheIdentity(t *testing.T) {
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
	got := l.enroll(t, "--force")
	if got.code == 0 || got.code == 2 {
		t.Fatalf("enroll --force on an accepted name: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout+got.stderr, "keeps the certificate and key it had") {
		t.Errorf("the refusal does not say the identity was kept:\n%s%s", got.stdout, got.stderr)
	}
	l.pairMatches(t, "after a refused --force")
	if l.files.Exists(pki.NodeKeyFile + ".pending") {
		t.Error("a refused key was kept as pending")
	}
	if again := l.enroll(t); again.code != 0 {
		t.Errorf("a plain enroll after the refusal: exit %d\n%s%s", again.code, again.stdout, again.stderr)
	}
}

// **A hub that fails keeps the new key** (DIVERGENCE 5.400). The hub
// answered its own failures, a record it could not read among them, with
// a 400 carrying the failure's text. A 4xx is a refusal to the node, so
// `enroll --force` deleted the key it was enrolling, and the node was
// told something about the hub's key store it had no business knowing.
// Now the hub says 500 and keeps the detail, and the node keeps the key
// for the next attempt, which a plain `enroll` makes once the hub is
// mended.
func TestAnEnrollForceAgainstAFailingHubKeepsTheNewKey(t *testing.T) {
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

	// The node is rebuilt, and the hub's record for it is damaged: not
	// JSON, which fails the same way as root.
	record := filepath.Join(l.keys, l.nodeID+".json")
	if err := os.WriteFile(record, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := l.enroll(t, "--force")
	out := got.stdout + got.stderr
	if got.code == 0 || got.code == 2 {
		t.Fatalf("enroll --force against a failing hub: exit %d\n%s", got.code, out)
	}
	if strings.Contains(out, l.keys) || strings.Contains(out, "not readable") {
		t.Errorf("the hub's own error reached the node:\n%s", out)
	}
	if strings.Contains(out, "discarded") || !l.files.Exists(pki.NodeKeyFile+".pending") {
		t.Fatalf("the new key was dropped for the hub's failure:\n%s", out)
	}
	l.pairMatches(t, "after a failing hub")

	// Mended: the record is gone, as an operator deleting it would leave
	// it, and a plain enroll carries on with the key that was kept.
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	pendingKey, err := l.files.ReadKey(pki.NodeKeyFile + ".pending")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := pki.FingerprintKey(pendingKey.Public())
	if again := l.enroll(t); again.code != 2 {
		t.Fatalf("enroll once the hub is mended: exit %d\n%s%s", again.code, again.stdout, again.stderr)
	}
	if fp := l.pendingFingerprint(t); fp != want {
		t.Errorf("the hub holds %s, not the key that was kept (%s)", fp, want)
	}
}
