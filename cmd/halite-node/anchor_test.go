package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/keystore"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/nodeevidence"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// anchorLab is a real hub -- hub.Server, its TLS listener, its anchor
// store -- and the key material of one node enrolled with it.
//
// Real rather than a fake handler, because the property under test spans
// both programs: the node's report, the hub's record and signature, and
// the node checking that signature against the CA it pinned. A fake hub
// would test the node against this file's idea of the hub.
type anchorLab struct {
	server  *hub.Server
	ca      *pki.CA
	addr    string
	anchors string
	pkiDir  string
	nodeID  string
}

func newAnchorLab(t *testing.T, nodeID string) *anchorLab {
	t.Helper()
	ca, err := pki.NewCA(pki.ECDSAP256, "anchor test CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	pkiDir := t.TempDir()
	files := pki.Files{Dir: pkiDir}
	nodeKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := pki.NewNodeCSR(nodeKey, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := pki.DecodeCSR(pki.EncodeCSR(csrDER))
	if err != nil {
		t.Fatal(err)
	}
	nodeDER, err := ca.IssueNode(csr, nodeID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteKey(pki.NodeKeyFile, nodeKey); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCert(pki.NodeCertFile, nodeDER); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCert(pki.CACertFile, ca.Cert.Raw); err != nil {
		t.Fatal(err)
	}

	hubKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	hubDER, err := ca.IssueHub(hubKey, []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := hub.Listen("127.0.0.1:0", tls.Certificate{Certificate: [][]byte{hubDER}, PrivateKey: hubKey},
		ca.Cert, transport.NewDenylist())
	if err != nil {
		t.Fatal(err)
	}
	anchorDir := t.TempDir()
	store, err := hub.OpenAnchorStore(anchorDir)
	if err != nil {
		t.Fatal(err)
	}
	srv := &hub.Server{
		Authority:    &keystore.Authority{CA: ca},
		Anchors:      store,
		Fleet:        hub.NewFleet(),
		PingInterval: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); ln.Close(); <-done })

	return &anchorLab{
		server: srv, ca: ca, addr: ln.Addr().String(),
		anchors: filepath.Join(anchorDir, nodeID+".jsonl"), pkiDir: pkiDir, nodeID: nodeID,
	}
}

func (l *anchorLab) client(t *testing.T) *transport.Client {
	t.Helper()
	pair, err := pki.Files{Dir: l.pkiDir}.KeyPair(pki.NodeCertFile, pki.NodeKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	return &transport.Client{HubURL: "https://" + l.addr, CA: l.ca.Cert, Cert: &pair, Timeout: 5 * time.Second}
}

func (l *anchorLab) lines(t *testing.T) []nodeevidence.Anchor {
	t.Helper()
	f, err := os.Open(l.anchors)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines, err := nodeevidence.ReadAnchors(f)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// waitFor polls until cond holds, for a test whose subject is a
// background worker that announces nothing.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func receiptsIn(recs []nodeevidence.Record) []nodeevidence.Record {
	var out []nodeevidence.Record
	for _, r := range recs {
		if r.Kind == nodeevidence.KindAnchorReceipt {
			out = append(out, r)
		}
	}
	return out
}

// A job's result is reported to a real hub, which records it and answers
// with a receipt the node files -- and filing the receipt does not set
// off another report.
//
// The job runs through the executor, not a call to recordJobResult,
// because "after every job" is a claim about the executor's path: a test
// that called the recording function directly would establish that it
// reports and nothing about whether anything calls it.
func TestAFinishedJobIsAnchoredAndTheReceiptFiled(t *testing.T) {
	lab := newAnchorLab(t, "web1.example")
	n := nodeForEvidence(t, "")
	client := lab.client(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.startAnchoring(ctx, func() *transport.Client { return client })

	n.refusals = make(chan *job.Return, 4)
	finished := make(chan *job.Return, 1)
	e := newExecutor(n, 4, func(ret *job.Return) { finished <- ret })
	n.executor = e
	stop := make(chan struct{})
	defer close(stop)
	go e.Run(stop)

	n.acceptJob(jobMessage(t, "20261006T120000000001"))
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the job did not finish")
	}

	waitFor(t, "an anchor receipt in the chain", func() bool {
		return len(receiptsIn(records(t, n))) > 0
	})
	// Long enough for a loop to show itself: each turn is a request to
	// a hub on loopback and one fsync on each side, a few milliseconds.
	time.Sleep(500 * time.Millisecond)

	recs := wantRecords(t, n, 3)
	if recs[1].Kind != nodeevidence.KindJobResult {
		t.Fatalf("record 2 is %q", recs[1].Kind)
	}
	receipt := recs[2]
	if receipt.Kind != nodeevidence.KindAnchorReceipt {
		t.Fatalf("record 3 is %q", receipt.Kind)
	}
	if receipt.Detail[nodeevidence.ReceiptAnchoredSeq] != "2" ||
		receipt.Detail[nodeevidence.ReceiptAnchoredHash] != recs[1].Hash {
		t.Errorf("the receipt is for %s %s, and the job's result is record 2 %s",
			receipt.Detail[nodeevidence.ReceiptAnchoredSeq], receipt.Detail[nodeevidence.ReceiptAnchoredHash], recs[1].Hash)
	}

	lines := lab.lines(t)
	if len(lines) != 1 {
		t.Fatalf("the hub holds %d lines; one report was made, so a receipt has set off another: %+v",
			len(lines), lines)
	}
	if lines[0].Seq != 2 || lines[0].Hash != recs[1].Hash || lines[0].Result != nodeevidence.AnchorAccepted {
		t.Errorf("the hub recorded %+v", lines[0])
	}

	check, err := nodeevidence.CheckAnchors(evidenceDirOf(t, n), lab.anchors, lines, lab.ca.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if !check.OK() {
		t.Errorf("the chain and the hub's record of it disagree: %+v", check.Breaks)
	}
}

// A hub that predates the endpoint answers 404. The node keeps running
// jobs, files nothing, and says so once rather than after every job.
func TestAnOldHubIsSaidOnceAndNothingIsFiled(t *testing.T) {
	stale, pkiDir := enrolledNode(t, "web1.example")
	pair, err := pki.Files{Dir: pkiDir}.KeyPair(pki.NodeCertFile, pki.NodeKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.Files{Dir: pkiDir}.ReadCert(pki.CACertFile)
	if err != nil {
		t.Fatal(err)
	}
	client := &transport.Client{HubURL: "https://" + stale.addr, CA: ca, Cert: &pair, Timeout: 5 * time.Second}

	n := nodeForEvidence(t, "")
	var logged bytes.Buffer
	n.log = testLogger(t, &logged)
	n.recordEvidence(nodeevidence.KindStart, map[string]string{"version": "test"})
	for i := 0; i < 3; i++ {
		n.reportHead(context.Background(), client)
	}
	if got := strings.Count(logged.String(), "does not keep evidence anchors"); got != 1 {
		t.Errorf("an old hub was reported %d times in three reports:\n%s", got, logged.String())
	}
	if len(receiptsIn(records(t, n))) != 0 {
		t.Error("a receipt was filed from a hub that gave none")
	}
}

// A receipt that does not verify against the CA this node pinned is not
// filed. The hub here signs with a CA of its own that is not the one
// that issued the certificates, which is what a hub whose signing key
// was swapped looks like from the node.
func TestAReceiptThatDoesNotVerifyIsNotFiled(t *testing.T) {
	lab := newAnchorLab(t, "web1.example")
	impostor, err := pki.NewCA(pki.ECDSAP256, "not the enrollment CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	lab.server.Authority = &keystore.Authority{CA: impostor}

	n := nodeForEvidence(t, "")
	var logged bytes.Buffer
	n.log = testLogger(t, &logged)
	n.recordEvidence(nodeevidence.KindStart, map[string]string{"version": "test"})
	n.reportHead(context.Background(), lab.client(t))

	if len(lab.lines(t)) != 1 {
		t.Fatal("the hub did not record the head, so this test is not testing the node's check")
	}
	if got := receiptsIn(records(t, n)); len(got) != 0 {
		t.Errorf("a receipt signed by another CA was filed: %+v", got)
	}
	if !strings.Contains(logged.String(), "does not verify against the CA this node pins") {
		t.Errorf("the refusal was not logged:\n%s", logged.String())
	}
}

// The whole path, as an operator runs it: `halite-node connect` against
// a real hub reports its head when the stream opens and again after a
// job, and `verify-evidence --anchors` with the hub's file agrees with
// the chain the agent wrote.
//
// A subprocess, because the claim is about runConnect: that the stream's
// opening is what triggers a report and that the reporter is started at
// all. Everything below it is tested in-process above.
func TestTheAgentAnchorsOnConnectAndAfterAJob(t *testing.T) {
	lab := newAnchorLab(t, "web1.example")
	root := t.TempDir()
	cfg := writeConfig(t, root, "node_id: web1.example\n"+
		"hub: "+lab.addr+"\n"+
		"pki_dir: "+lab.pkiDir+"\n"+
		"state_dir: "+filepath.Join(root, "state")+"\n"+
		"cache_dir: "+filepath.Join(root, "cache")+"\n"+
		"pillar_roots:\n  base:\n    - "+t.TempDir()+"\n"+
		"file_roots:\n  base:\n    - "+t.TempDir()+"\n")
	common := []string{"--root", root, "--config", cfg}

	agent := exec.Command(os.Args[0], append([]string{"connect", "--log-fmt", "console"}, common...)...)
	agent.Env = append(os.Environ(), reexec+"=1")
	var agentLog bytes.Buffer
	agent.Stdout, agent.Stderr = &agentLog, &agentLog
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stopAgent := func() {
		if stopped {
			return
		}
		stopped = true
		_ = agent.Process.Signal(os.Interrupt)
		waited := make(chan error, 1)
		go func() { waited <- agent.Wait() }()
		select {
		case <-waited:
		case <-time.After(10 * time.Second):
			_ = agent.Process.Kill()
			<-waited
		}
	}
	defer func() {
		stopAgent()
		if t.Failed() {
			t.Logf("the agent's log:\n%s", agentLog.String())
		}
	}()

	// On connect: the agent writes its start and configuration records
	// and the stream's opening reports them.
	waitFor(t, "the hub to record the agent's head on connect", func() bool {
		return len(lab.lines(t)) >= 1
	})
	first := lab.lines(t)[0]
	if first.Result != nodeevidence.AnchorAccepted || first.Seq < 2 {
		t.Fatalf("the report on connect was %+v; the agent writes two records before it connects", first)
	}

	// After a job: one dispatched straight down the stream.
	nonce, err := job.Nonce()
	if err != nil {
		t.Fatal(err)
	}
	if !lab.server.Fleet.Send("web1.example", transport.Message{
		T: transport.MsgJob, JID: "20261006T120000000009", Fun: "test.ping", Nonce: nonce,
		Expires: time.Now().Add(job.DefaultTTL).UTC().Format(time.RFC3339Nano),
	}) {
		t.Fatal("the node is not on the hub's stream")
	}
	waitFor(t, "the hub to record the head after the job", func() bool {
		lines := lab.lines(t)
		return len(lines) >= 2 && lines[len(lines)-1].Seq > first.Seq
	})
	// And for the node to have filed the receipt for it. The hub writes
	// its line before it answers, so there is a moment when the hub has
	// the head and the node does not yet have the receipt; stopping the
	// agent in it is a node that never files one, which is correct and
	// is not what this test is about. A full run of the package failed
	// once without this wait; the failing assertion was not captured,
	// and a 300ms sleep put into that window makes the test fail on
	// "did not file a receipt for each report" without the wait and pass
	// with it.
	evidenceDir := filepath.Join(root, "state", "evidence")
	waitFor(t, "the agent to file both receipts", func() bool {
		res, err := nodeevidence.Verify(evidenceDir)
		if err != nil || res.Records == 0 {
			return false
		}
		check, err := nodeevidence.CheckAnchors(evidenceDir, "", nil, lab.ca.Cert)
		return err == nil && check.Found >= 2
	})
	stopAgent()

	for _, l := range lab.lines(t) {
		if l.Result != nodeevidence.AnchorAccepted {
			t.Errorf("an honest agent's report was not accepted: %+v", l)
		}
	}

	got := run(t, append([]string{"verify-evidence", "--anchors", lab.anchors}, common...)...)
	if got.code != 0 {
		t.Fatalf("verify-evidence --anchors exited %d against the chain the agent wrote:\n%s%s",
			got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "It also agrees with the hub") {
		t.Errorf("verify-evidence did not say the chain agrees with the hub:\n%s", got.stdout)
	}
	// One receipt per accepted head: the report on connect and the one
	// after the job.
	if !strings.Contains(got.stdout, "receipts:  2 checked") {
		t.Errorf("the agent did not file a receipt for each report:\n%s", got.stdout)
	}
	t.Logf("verify-evidence --anchors:\n%s", got.stdout)

	// The attacker with root: throw the chain away and write a new one
	// from its first record, with the same number of records and none of
	// the jobs, through the same code the agent uses. It verifies on its
	// own, and the hub's file is what says it is not the chain that was
	// reported.
	if err := os.RemoveAll(evidenceDir); err != nil {
		t.Fatal(err)
	}
	forged, err := nodeevidence.Open(evidenceDir, nodeevidence.Options{NodeID: "web1.example"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if err := forged.Append(nodeevidence.KindStart, map[string]string{"version": "innocent"}); err != nil {
			t.Fatal(err)
		}
	}
	forged.Close()

	alone := run(t, append([]string{"verify-evidence"}, common...)...)
	if alone.code != 0 {
		t.Fatalf("the forgery does not verify on its own, so this is not the attack: %s%s", alone.stdout, alone.stderr)
	}
	against := run(t, append([]string{"verify-evidence", "--anchors", lab.anchors}, common...)...)
	if against.code == 0 {
		t.Fatalf("a chain rewritten from its first record passed against the hub's record:\n%s", against.stdout)
	}
	if !strings.Contains(against.stdout, "rewritten since it was reported") {
		t.Errorf("the break does not say the chain was rewritten:\n%s", against.stdout)
	}
	t.Logf("verify-evidence --anchors against a forgery:\n%s", against.stdout)
}

// testLogger writes at info and above into w, which is the level an
// operator's log keeps: a repeat demoted to debug is one they never see.
func testLogger(t *testing.T, w *bytes.Buffer) *hlog.Logger {
	t.Helper()
	l, err := hlog.New(hlog.Options{Level: hlog.Info, Format: hlog.JSON, Stderr: w})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// A report the hub rate-limits is routine for a node finishing jobs
// faster than the hub's limit: nothing is filed, nothing above debug is
// said, and the next report the hub takes is filed as usual. A warning
// per refused report would make a busy node's log the flood. DIVERGENCE
// 5.231.
func TestARateLimitedReportIsQuietAndTheNextIsFiled(t *testing.T) {
	lab := newAnchorLab(t, "web1.example")
	now := time.Now()
	lab.server.Now = func() time.Time { return now }
	lab.server.Anchors.Rate, lab.server.Anchors.Burst = 0.5, 1

	n := nodeForEvidence(t, "")
	var logged bytes.Buffer
	n.log = testLogger(t, &logged)
	client := lab.client(t)

	n.recordEvidence(nodeevidence.KindStart, map[string]string{"version": "test"})
	n.reportHead(context.Background(), client)
	n.recordEvidence(nodeevidence.KindStop, map[string]string{"reason": "test"})
	n.reportHead(context.Background(), client) // over the rate

	if got := receiptsIn(records(t, n)); len(got) != 1 {
		t.Fatalf("%d receipts filed; the rate-limited report must file none", len(got))
	}
	if strings.Contains(logged.String(), `"level":"warn"`) || strings.Contains(logged.String(), `"level":"error"`) {
		t.Errorf("a rate-limited report was said above debug:\n%s", logged.String())
	}

	now = now.Add(3 * time.Second)
	n.reportHead(context.Background(), client)
	if got := receiptsIn(records(t, n)); len(got) != 2 {
		t.Errorf("after the bucket refilled, %d receipts; the report should have been filed", len(got))
	}
}

// A report in flight when the agent stops. The hub has answered and the
// receipt is about to be filed, and the log is closed under it: a
// shutdown that lands in the few milliseconds between the response and
// the append.
//
// The close is made from the client's Observe hook, which runs after the
// exchange and before AnchorEvidence returns, so the interleaving is
// exact rather than a sleep that usually lands there.
//
// What must hold: the chain is sound and ends at the stop record, the
// receipt is not in it, and the node does not call that a lost record --
// halite_node_evidence_failures_total is what HaliteNodeEvidenceNotWritten
// pages on, and a clean shutdown must not page. DIVERGENCE 5.237.
func TestAReceiptThatArrivesAfterTheLogClosesIsNotALostRecord(t *testing.T) {
	lab := newAnchorLab(t, "web1.example")
	// A metrics_listen, because a node without one keeps no registry and
	// counts nothing, which would make the counter assertion below pass
	// whatever the node did.
	n := nodeForEvidence(t, "metrics_listen: 127.0.0.1:0\n")
	var logged bytes.Buffer
	n.log = testLogger(t, &logged)
	n.recordEvidence(nodeevidence.KindStart, map[string]string{"version": "test"})

	client := lab.client(t)
	stopped := false
	client.Observe = func(route string, status int, _ time.Duration) {
		if route == transport.PathEvidenceAnchor && !stopped {
			stopped = true
			n.stopEvidence("the agent stopped")
		}
	}
	n.reportHead(context.Background(), client)

	if !stopped {
		t.Fatal("the report was never made, so the log was never closed under it")
	}
	if len(lab.lines(t)) != 1 {
		t.Fatal("the hub did not record the head, so no receipt was in flight")
	}
	recs := records(t, n)
	if got := receiptsIn(recs); len(got) != 0 {
		t.Errorf("a receipt was filed after the log closed: %+v", got)
	}
	if last := recs[len(recs)-1]; last.Kind != nodeevidence.KindStop {
		t.Errorf("the chain ends at %q, not at the stop record: %v", last.Kind, kindsOf(recs))
	}
	var expo bytes.Buffer
	if err := n.metrics.registry.Write(&expo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expo.String(), "# TYPE halite_node_evidence_failures_total counter") {
		t.Fatalf("the failure counter is not exported, so the check on it means nothing:\n%s", expo.String())
	}
	if strings.Contains(expo.String(), "halite_node_evidence_failures_total 1") {
		t.Errorf("a receipt that arrived late is counted as a record that could not be written:\n%s", expo.String())
	}
	if strings.Contains(logged.String(), "could not be written") {
		t.Errorf("a clean stop logged an error:\n%s", logged.String())
	}
}

// Only a receipt is excused when it arrives after the close. A job's
// result that does is a job with no entry in the record, which is what
// halite_node_evidence_failures_total counts and what
// HaliteNodeEvidenceNotWritten pages on; excusing every kind would turn
// the alert off for exactly the loss it exists to find.
func TestAJobRecordAfterTheLogClosesIsStillALostRecord(t *testing.T) {
	n := nodeForEvidence(t, "metrics_listen: 127.0.0.1:0\n")
	var logged bytes.Buffer
	n.log = testLogger(t, &logged)
	n.recordEvidence(nodeevidence.KindStart, map[string]string{"version": "test"})
	n.stopEvidence("the agent stopped")

	n.recordEvidence(nodeevidence.KindJobResult, map[string]string{"jid": "20261007T000000000001"})

	var expo bytes.Buffer
	if err := n.metrics.registry.Write(&expo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expo.String(), "halite_node_evidence_failures_total 1") {
		t.Errorf("a job result after the close was not counted as lost:\n%s", expo.String())
	}
	if !strings.Contains(logged.String(), "could not be written") {
		t.Errorf("a job result after the close was lost without an error:\n%s", logged.String())
	}
}
