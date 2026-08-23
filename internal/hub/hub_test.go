package hub

import (
	"context"
	"crypto"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/keystore"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// lab is a hub with its CA, its listener, and its denylist, all real.
type lab struct {
	server *Server
	files  pki.Files
	ca     *pki.CA
	denied *transport.Denylist
	url    string
	// root is the directory the hub serves, when a test gives it one.
	root string
}

func newLab(t *testing.T) *lab {
	t.Helper()
	dir := t.TempDir()
	files := pki.Files{Dir: dir}
	ca, err := files.CreateCA(pki.ECDSAP256, "halite test CA", 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	hubKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	hubDER, err := ca.IssueHub(hubKey, []string{"localhost", "127.0.0.1"}, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteKey(pki.HubKeyFile, hubKey); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCert(pki.HubCertFile, hubDER); err != nil {
		t.Fatal(err)
	}
	pair, err := files.KeyPair(pki.HubCertFile, pki.HubKeyFile)
	if err != nil {
		t.Fatal(err)
	}

	store, err := keystore.Open(dir + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	// The hub logs into the test's own output. Without this a handler
	// that says "its log says why" says it into nothing, and
	// diagnosing a failing test means adding the logger first.
	logger, err := hlog.New(hlog.Options{Level: hlog.Debug, Format: hlog.Console, Stderr: testWriter{t}})
	if err != nil {
		t.Fatal(err)
	}
	denied := transport.NewDenylist()
	srv := &Server{
		Log: logger,
		Authority: &keystore.Authority{
			Store:    store,
			CA:       ca,
			Mode:     keystore.ModeManual,
			Revoker:  denied,
			Lifetime: keystore.DefaultLifetime,
		},
		PingInterval: 20 * time.Millisecond,
	}

	ln, err := Listen("127.0.0.1:0", pair, ca.Cert, denied)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		ln.Close()
		<-done
	})

	return &lab{
		server: srv,
		files:  files,
		ca:     ca,
		denied: denied,
		url:    "https://" + net.JoinHostPort("localhost", port(t, ln.Addr().String())),
	}
}

// testWriter sends the hub's log to the test's output, where it is
// shown only for a test that fails.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("hub: %s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func port(t *testing.T, addr string) string {
	t.Helper()
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// node is the client side, with the CA pinned as an operator would
// have delivered it.
func (l *lab) node(t *testing.T) (*transport.Client, crypto.Signer) {
	t.Helper()
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	return &transport.Client{HubURL: l.url, CA: l.ca.Cert, Timeout: 5 * time.Second}, key
}

// enrolled takes a node all the way in: request, operator accept,
// collect, and load the certificate for mutual TLS.
func (l *lab) enrolled(t *testing.T, nodeID string) *transport.Client {
	t.Helper()
	client, key := l.node(t)
	ctx := context.Background()

	_, err := client.Enroll(ctx, key, nodeID, "")
	if !errors.Is(err, transport.ErrPending) {
		t.Fatalf("a first request should be pending, got %v", err)
	}
	if _, err := l.server.Authority.Accept(nodeID); err != nil {
		t.Fatal(err)
	}
	got, err := client.Enroll(ctx, key, nodeID, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(got.CertPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	client.Cert = &pair
	client.Reset()
	return client
}

// The lab dials by name, so a certificate that covered no address
// looked fine here for as long as nothing dialled one. A node given a
// hub as `hub: 127.0.0.1` dials an address.
func TestTheHubIsReachableByAddressAsWellAsByName(t *testing.T) {
	l := newLab(t)
	client, _ := l.node(t)
	client.HubURL = strings.Replace(l.url, "localhost", "127.0.0.1", 1)
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("dialling the hub by address: %v", err)
	}
}

func TestHealthNeedsNoCertificate(t *testing.T) {
	l := newLab(t)
	client, _ := l.node(t)
	body, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "halite-hub ") || !strings.HasSuffix(body, "ok") {
		t.Errorf("health answered %q", body)
	}
}

// The full manual enrollment of SPEC 7.3, over the wire.
func TestANodeEnrolsAndIsAcceptedByAnOperator(t *testing.T) {
	l := newLab(t)
	client, key := l.node(t)
	ctx := context.Background()

	first, err := client.Enroll(ctx, key, "web1.example", "")
	if !errors.Is(err, transport.ErrPending) {
		t.Fatalf("a manual enrollment should be pending, got %v", err)
	}
	if first.Fingerprint == "" {
		t.Fatal("the node needs the fingerprint to compare out of band")
	}

	// The hub and the node have to agree about the fingerprint, or the
	// out-of-band comparison compares nothing.
	rec, err := l.server.Authority.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Fingerprint != first.Fingerprint {
		t.Errorf("the hub says %s and the node says %s", rec.Fingerprint, first.Fingerprint)
	}
	if rec.State != keystore.Pending {
		t.Fatalf("the record is %s", rec.State)
	}

	if _, err := l.server.Authority.Accept("web1.example"); err != nil {
		t.Fatal(err)
	}
	got, err := client.Enroll(ctx, key, "web1.example", "")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := pki.DecodeCert(got.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	id, err := pki.NodeIDFromCert(cert)
	if err != nil || id != "web1.example" {
		t.Fatalf("the issued certificate names %q (%v)", id, err)
	}
	if len(got.CAPEM) == 0 {
		t.Error("the node should receive the CA it was issued by")
	}
}

// An enrolled node's connection carries its identity, and the hub reads
// it off the certificate rather than out of the body.
func TestTheStreamIsIdentifiedByTheCertificate(t *testing.T) {
	l := newLab(t)
	client := l.enrolled(t, "web1.example")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pings := make(chan int64, 4)
	jobs := make(chan transport.Message, 4)
	go func() {
		client.Subscribe(ctx, transport.SubscribeRequest{NodeID: "web1.example", Version: "test"},
			func(msg transport.Message) error {
				switch msg.T {
				case transport.MsgPing:
					select {
					case pings <- msg.Seq:
					default:
					}
				case transport.MsgJob:
					jobs <- msg
				}
				return nil
			})
	}()

	select {
	case <-pings:
	case <-ctx.Done():
		t.Fatal("no ping arrived on the subscribe stream")
	}

	// The hub knows who is connected, by name.
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := l.server.Fleet.Connected()["web1.example"]; ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hub does not have web1.example connected: %v", l.server.Fleet.Connected())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A job addressed to that name reaches that node.
	sent := transport.Message{
		T: transport.MsgJob, JID: "20260822T120000000000", Fun: "test.ping",
		Arg: []string{"one"}, Env: "base",
	}
	if !l.server.Fleet.Send("web1.example", sent) {
		t.Fatal("the hub would not send to a connected node")
	}
	if l.server.Fleet.Send("db1.example", sent) {
		t.Error("the hub claimed to send to a node that is not connected")
	}
	select {
	case got := <-jobs:
		if got.JID != sent.JID || got.Fun != sent.Fun || len(got.Arg) != 1 || got.Arg[0] != "one" {
			t.Errorf("the job arrived as %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("the job never arrived")
	}
}

// A node that claims a different name in the body than in its
// certificate is refused rather than quietly overruled.
func TestTheBodyCannotRenameTheNode(t *testing.T) {
	l := newLab(t)
	client := l.enrolled(t, "web1.example")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := client.Subscribe(ctx, transport.SubscribeRequest{NodeID: "db1.example"},
		func(transport.Message) error { return nil })
	if err == nil {
		t.Fatal("a node subscribed under a name that is not its own")
	}
	if !strings.Contains(err.Error(), "web1.example") {
		t.Errorf("the refusal should name the certificate's identity: %v", err)
	}
}

// SPEC 7.4: revocation reaches the live stream and the next handshake.
func TestRevocationEndsTheStreamAndTheNextConnection(t *testing.T) {
	l := newLab(t)
	client := l.enrolled(t, "web1.example")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	revoked := make(chan string, 1)
	connected := make(chan struct{})
	go func() {
		client.Subscribe(ctx, transport.SubscribeRequest{NodeID: "web1.example"},
			func(msg transport.Message) error {
				switch msg.T {
				case transport.MsgPing:
					select {
					case <-connected:
					default:
						close(connected)
					}
				case transport.MsgRevoke:
					revoked <- msg.Reason
				}
				return nil
			})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("the node never connected")
	}

	if err := l.server.Revoke("web1.example", "decommissioned"); err != nil {
		t.Fatal(err)
	}
	select {
	case reason := <-revoked:
		if reason != "decommissioned" {
			t.Errorf("the node was told %q", reason)
		}
	case <-ctx.Done():
		t.Fatal("the node was never told it had been revoked")
	}

	// And the next connection does not complete a handshake at all.
	client.Reset()
	if _, err := client.Health(context.Background()); err == nil {
		t.Error("a revoked certificate still completed a handshake")
	}
}

// A hub that restarts must not forget what it revoked, and an operator
// running `keys revoke` in another process must reach the running one.
func TestTheServerFollowsTheStoreForRevocations(t *testing.T) {
	l := newLab(t)
	client := l.enrolled(t, "web1.example")
	rec, err := l.server.Authority.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}

	// A second Authority over the same directory is what the operator
	// command line is: same store, different process, no denylist.
	other := &keystore.Authority{Store: l.server.Authority.Store, CA: l.ca, Mode: keystore.ModeManual}
	if _, err := other.Revoke("web1.example", "revoked from the command line"); err != nil {
		t.Fatal(err)
	}
	if _, denied := l.denied.Revoked(rec.Serial); denied {
		t.Fatal("the running hub cannot have seen this yet; the test proves nothing")
	}

	if err := l.server.reconcileOnce(); err != nil {
		t.Fatal(err)
	}
	if _, denied := l.denied.Revoked(rec.Serial); !denied {
		t.Error("the running hub did not follow the store")
	}
	client.Reset()
	if _, err := client.Health(context.Background()); err == nil {
		t.Error("a revoked certificate still completed a handshake")
	}
}

// The lab found this and the tests above did not: they call Reset
// before checking, which drops the pooled connection and forces a fresh
// handshake. A real node reconnecting reuses the HTTP/2 connection it
// already has, and the transport never sees a second ClientHello to
// refuse -- so revocation has to be enforced per request as well.
func TestARevokedNodeCannotKeepUsingAnEstablishedConnection(t *testing.T) {
	l := newLab(t)
	client := l.enrolled(t, "web1.example")

	if _, err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := l.server.Revoke("web1.example", "decommissioned"); err != nil {
		t.Fatal(err)
	}

	// No Reset: this is the same client, over the connection it opened
	// before it was revoked.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := client.Subscribe(ctx, transport.SubscribeRequest{NodeID: "web1.example"},
		func(transport.Message) error { return nil })
	if err == nil {
		t.Fatal("a revoked node subscribed over its existing connection")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

func TestTokenEnrollmentIssuesWithoutAnOperator(t *testing.T) {
	l := newLab(t)
	l.server.Authority.Mode = keystore.ModeToken
	_, secret, err := l.server.Authority.Store.MintToken(keystore.TokenOptions{
		TTL:      time.Hour,
		NodeGlob: "web*.example",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	client, key := l.node(t)
	got, err := client.Enroll(context.Background(), key, "web1.example", secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CertPEM) == 0 {
		t.Fatal("a valid token should have issued a certificate")
	}

	// The same token again admits nothing, and the node is told it was
	// refused without being told why.
	other, otherKey := l.node(t)
	_, err = other.Enroll(context.Background(), otherKey, "web2.example", secret)
	if err == nil {
		t.Fatal("a single-use token admitted a second node")
	}
	if strings.Contains(err.Error(), "spent") || strings.Contains(err.Error(), "uses") {
		t.Errorf("the refusal told the node more than it should: %v", err)
	}
}

// A renewal supersedes the certificate the node's stream was opened
// with, and the hub revokes the old serial. A stream left running on it
// would be authenticated by a serial the hub has just denied, so the
// node is asked to come back.
func TestRenewalEndsTheStreamSoTheNodeReloads(t *testing.T) {
	l := newLab(t)
	client := l.enrolled(t, "web1.example")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connected := make(chan struct{})
	reload := make(chan string, 1)
	ended := make(chan error, 1)
	go func() {
		ended <- client.Subscribe(ctx, transport.SubscribeRequest{NodeID: "web1.example"},
			func(msg transport.Message) error {
				switch msg.T {
				case transport.MsgPing:
					select {
					case <-connected:
					default:
						close(connected)
					}
				case transport.MsgReload:
					reload <- msg.Reason
				}
				return nil
			})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("the node never connected")
	}

	newKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Renew(context.Background(), newKey, "web1.example"); err != nil {
		t.Fatal(err)
	}
	select {
	case reason := <-reload:
		if reason == "" {
			t.Error("the node should be told why it is being asked back")
		}
	case <-ctx.Done():
		t.Fatal("the stream outlived the certificate it was opened with")
	}
	select {
	case err := <-ended:
		if err != nil {
			t.Errorf("a reload should end the stream cleanly: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the stream did not end")
	}
}

func TestRenewalOverTheWire(t *testing.T) {
	l := newLab(t)
	client := l.enrolled(t, "web1.example")

	before, err := l.server.Authority.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Renew(context.Background(), newKey, "web1.example")
	if err != nil {
		t.Fatal(err)
	}
	after, err := l.server.Authority.Store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if after.Serial == before.Serial {
		t.Error("renewal should have issued a new serial")
	}
	if len(got.CertPEM) == 0 {
		t.Fatal("renewal returned no certificate")
	}
	// The new key works on the wire, which is the only proof that
	// matters.
	keyPEM, err := pki.EncodeKey(newKey)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(got.CertPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	client.Cert = &pair
	client.Reset()
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("the renewed certificate does not work: %v", err)
	}
}

// An unenrolled peer can reach health and nothing else.
func TestAnUnenrolledNodeCannotSubscribe(t *testing.T) {
	l := newLab(t)
	client, _ := l.node(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := client.Subscribe(ctx, transport.SubscribeRequest{NodeID: "web1.example"},
		func(transport.Message) error { return nil })
	if err == nil {
		t.Fatal("a node with no certificate subscribed")
	}
}
