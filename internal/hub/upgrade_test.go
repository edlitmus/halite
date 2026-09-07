package hub

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// SPEC 31's Upgrade row, the halves that live in the hub.
// upgrade:version-skew
// upgrade:cert-rotation
//
// # What this establishes, and what was assumed before it
//
// Salt requires its server upgraded first, because an agent there speaks
// a protocol the server defines — master and minion in its own words. // lexicon:allow
// That is twelve years of muscle memory for anyone arriving from it, and
// it does not transfer: halite's wire is tolerant in *both* directions,
// so neither order is forced.
//
//   - A newer hub talking to an older node: an unknown message type
//     reaches the node's `default:` branch, which logs it and carries
//     on. The stream stays open.
//   - A newer node talking to an older hub: nothing in this tree calls
//     `DisallowUnknownFields`, so a request carrying fields the hub has
//     never heard of is accepted and the fields ignored.
//
// **That tolerance was accidental.** It falls out of `encoding/json`'s
// defaults and one `default:` branch, and until these tests nothing
// recorded it as a guarantee or would have noticed it being taken away.
// It is a guarantee now.
//
// The one place skew is fatal is the ALPN. SPEC 6.4 makes `halite/1`
// mandatory — "a peer that does not offer it is rejected at handshake" —
// so bumping it breaks both directions at once, and that, not the
// message shapes, is where an upgrade order would come from. It is
// frozen at 1.

// A node older than the hub is accepted, including one old enough to
// send no schema at all.
//
// The ordinary case during a hub-first upgrade, and the one that must
// not break: every node in an estate is briefly older than its hub.
func TestUpgradeANodeOlderThanTheHubIsAccepted(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	client := l.enrolled(t, "web1.example")
	op := l.operator(t, "ed")

	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Offline: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}

	// No schema and no version, which is what a node from before either
	// field existed sends.
	if err := client.Return(context.Background(), job.Return{
		JID: job.ID(res.JID), NodeID: "web1.example", Fun: "test.ping", Success: true,
	}); err != nil {
		t.Fatalf("a return from an older node was refused: %v", err)
	}

	status, err := op.JobStatus(context.Background(), res.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Returns) != 1 {
		t.Fatalf("%d returns from an older node", len(status.Returns))
	}
	// And it is recorded as this build's shape rather than as blank,
	// so a dashboard reading the schema sees a version rather than an
	// empty string.
	rets, err := l.server.Jobs.Returns(job.ID(res.JID))
	if err != nil {
		t.Fatal(err)
	}
	if len(rets) != 1 || rets[0].Schema != job.ReturnSchema {
		t.Errorf("an unschema'd return was stored as %q, want %q", rets[0].Schema, job.ReturnSchema)
	}
}

// A node newer than the hub is accepted, and the hub says so.
//
// Accepted, because a return is the only evidence that work already
// happened on a node: refusing it loses that evidence and the node has
// nowhere to put it again, so the job would look unanswered for ever.
// The same argument `doctor`'s disk-full check makes — a failure after
// the instruction has gone out must not be turned into a second
// untruth.
//
// Said so, because SPEC 9.4 freezes `halite.ret/1` "so a dashboard
// built on it keeps working", and until now the freeze had nothing
// behind it: an unknown schema was stored verbatim and nothing noticed.
func TestUpgradeANodeNewerThanTheHubIsAcceptedAndSaidSo(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	client := l.enrolled(t, "web1.example")
	op := l.operator(t, "ed")

	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Offline: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Return(context.Background(), job.Return{
		JID: job.ID(res.JID), NodeID: "web1.example", Fun: "test.ping",
		Success: true, Schema: "halite.ret/2", NodeVersion: "99.0.0",
	}); err != nil {
		t.Fatalf("a return from a newer node was refused, so the work it recorded is "+
			"lost and the job looks unanswered: %v", err)
	}

	status, err := op.JobStatus(context.Background(), res.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Returns) != 1 {
		t.Fatalf("%d returns from a newer node", len(status.Returns))
	}
	// Stored as it arrived rather than rewritten to this build's
	// version, so what actually happened is recoverable.
	rets, err := l.server.Jobs.Returns(job.ID(res.JID))
	if err != nil {
		t.Fatal(err)
	}
	if rets[0].Schema != "halite.ret/2" {
		t.Errorf("the return's schema was rewritten to %q; what arrived is no longer "+
			"recoverable", rets[0].Schema)
	}
}

// A request carrying fields the hub has never heard of is accepted.
//
// This is the other half of the tolerance, and the half that comes for
// free from `encoding/json` — which is exactly why it is worth a test.
// Nothing in the tree calls `DisallowUnknownFields`, and nothing said
// that was deliberate; a change that added it would break every newer
// node talking to an older hub, silently, at the moment of an upgrade.
func TestUpgradeUnknownRequestFieldsAreIgnored(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.enrolled(t, "web1.example")
	op := l.operator(t, "ed")

	// A submit request as a newer halite would send it: everything this
	// build knows, plus two things it does not.
	body := map[string]any{
		"target":              "*",
		"fun":                 "test.ping",
		"offline":             "queue",
		"policy_digest":       "sha256:deadbeef",
		"future_batch_policy": map[string]any{"mode": "adaptive"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var req transport.SubmitRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("a request from a newer halite did not decode: %v", err)
	}
	res, err := op.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("a request carrying unknown fields was refused: %v", err)
	}
	if res.JID == "" {
		t.Error("the job was not dispatched")
	}
	if len(res.Absent) != 1 {
		t.Errorf("the fields this build does know were not honoured: %+v", res)
	}
}

// The ALPN is the one place skew is fatal, and it is frozen.
//
// Everything above is tolerance; this is the boundary of it. SPEC 6.4
// makes `halite/1` mandatory, so a peer offering anything else is
// refused at the handshake in both directions at once — which is where
// an upgrade *order* would come from if the identifier ever moved. It
// has not, and this test is what would notice.
func TestUpgradeTheALPNIsTheOnePlaceSkewIsFatal(t *testing.T) {
	if transport.ALPN != "halite/1" {
		t.Fatalf("the ALPN is %q. Moving it makes old and new peers unable to speak at "+
			"all, in both directions, which is the one thing that would force an upgrade "+
			"order. If that is deliberate, SPEC 6.4, DIVERGENCE 4.13 and this test all "+
			"need rewriting together.", transport.ALPN)
	}
}

// A node's certificate is rotated while the hub is running, and the
// node keeps working across it.
//
// SPEC 31 asks for this "across an upgrade", and the reason it belongs
// in the upgrade row rather than the enrollment one is what the two do
// together: an upgrade restarts the hub, a restart drops every stream,
// and a node reconnecting during its own renewal window is the case
// where a certificate could lapse between the two. What this
// establishes is that a renewal issues a usable certificate and that
// the old one keeps working until it is replaced — so the order of the
// two operations does not matter.
func TestUpgradeACertificateRotatesWithoutLosingTheNode(t *testing.T) {
	l := newLab(t).withJobs(t)
	client := l.enrolled(t, "web1.example")
	op := l.operator(t, "ed")

	before := certOf(t, client)

	// The old certificate works.
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("the node could not reach the hub before renewing: %v", err)
	}

	newKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := client.Renew(context.Background(), newKey, "web1.example")
	if err != nil {
		t.Fatalf("renewing across an upgrade failed: %v", err)
	}
	if len(rotated.CertPEM) == 0 {
		t.Fatal("the renewal returned no certificate")
	}

	// The new one is a different certificate for the same node, and it
	// outlives the old.
	after := parseCertPEM(t, rotated.CertPEM)
	if after.SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Error("renewing returned the same certificate")
	}
	if id, err := pki.NodeIDFromCert(after); err != nil || id != "web1.example" {
		t.Errorf("the renewed certificate is for %q (%v)", id, err)
	}
	// No *earlier*, rather than strictly later. A renewal issued in the
	// same second as the original lands on the same expiry, because both
	// are NotBefore plus the same lifetime — which is what happens in a
	// test and never on a node, where a renewal happens near the end of
	// a window. What must never happen is the expiry moving backwards.
	if after.NotAfter.Before(before.NotAfter) {
		t.Errorf("the renewed certificate expires at %s, earlier than the old one at %s",
			after.NotAfter, before.NotAfter)
	}

	// And the old certificate is still usable until it expires, which
	// is what makes the order of "restart the hub" and "renew the node"
	// not matter. A node that lost its access the moment a new
	// certificate was issued could not survive a renewal it did not
	// finish applying.
	if _, err := client.Health(context.Background()); err != nil {
		t.Errorf("the old certificate stopped working the moment a new one was issued, "+
			"so a node interrupted mid-renewal is locked out: %v", err)
	}
	// The operator's own connection is unaffected either way.
	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	}); err != nil {
		t.Errorf("a rotation broke the operator's connection: %v", err)
	}
}

// certOf reads the certificate a client is holding.
func certOf(t *testing.T, c *transport.Client) *x509.Certificate {
	t.Helper()
	if c.Cert == nil || len(c.Cert.Certificate) == 0 {
		t.Fatal("the client holds no certificate")
	}
	cert, err := x509.ParseCertificate(c.Cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func parseCertPEM(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	cert, err := pki.DecodeCert(pemBytes)
	if err != nil {
		t.Fatalf("the renewed certificate does not parse: %v", err)
	}
	return cert
}

// A hub that has been running long enough to see both is what an
// upgrade looks like from the middle, and neither node is refused.
func TestUpgradeAMixedFleetIsServedFromOneHub(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	older := l.enrolled(t, "web1.example")
	newer := l.enrolled(t, "web2.example")
	op := l.operator(t, "ed")

	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Offline: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}

	// N-1 answers with no schema; N+1 answers with one this hub does
	// not know. Both are recorded, and the job completes.
	if err := older.Return(context.Background(), job.Return{
		JID: job.ID(res.JID), NodeID: "web1.example", Fun: "test.ping", Success: true,
	}); err != nil {
		t.Fatalf("the older node's return was refused: %v", err)
	}
	if err := newer.Return(context.Background(), job.Return{
		JID: job.ID(res.JID), NodeID: "web2.example", Fun: "test.ping",
		Success: true, Schema: "halite.ret/2", NodeVersion: "99.0.0",
	}); err != nil {
		t.Fatalf("the newer node's return was refused: %v", err)
	}

	status, err := op.JobStatus(context.Background(), res.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Returns) != 2 {
		t.Fatalf("%d of 2 returns from a mixed fleet", len(status.Returns))
	}
	// The job is complete, so a mixed fleet does not leave jobs looking
	// half-answered for as long as an upgrade takes.
	waitFor(t, 5*time.Second, "the job to be marked complete", func() bool {
		j, err := l.server.Jobs.Get(job.ID(res.JID))
		return err == nil && j.State == job.Complete
	})

	// And the schemas are stored as they arrived, so which node was
	// which is answerable afterwards.
	rets, err := l.server.Jobs.Returns(job.ID(res.JID))
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]string{}
	for _, r := range rets {
		schemas[r.NodeID] = r.Schema
	}
	if schemas["web1.example"] != job.ReturnSchema {
		t.Errorf("the older node's return is stored as %q", schemas["web1.example"])
	}
	if schemas["web2.example"] != "halite.ret/2" {
		t.Errorf("the newer node's return is stored as %q", schemas["web2.example"])
	}
	if !strings.HasPrefix(schemas["web1.example"], "halite.ret/") {
		t.Errorf("a stored schema is not a return schema at all: %q", schemas["web1.example"])
	}
}
