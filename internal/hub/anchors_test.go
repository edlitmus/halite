package hub

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/nodeevidence"
	"github.com/edlitmus/halite/internal/transport"
)

const (
	headA = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	headB = "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// withAnchors gives the lab an anchor store and a registry, and returns
// the store's directory.
func (l *lab) withAnchors(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenAnchorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.server.Anchors = store
	l.server.Metrics = metrics.NewRegistry()
	return dir
}

func anchorLines(t *testing.T, dir, nodeID string) []nodeevidence.Anchor {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, nodeID+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
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

func refusedStatus(t *testing.T, err error) (int, string) {
	t.Helper()
	var refused *transport.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("expected a refusal, got %v", err)
	}
	return refused.Status, refused.Code
}

// A head is recorded, and the receipt the hub answers with verifies
// against the CA the node pinned at enrollment -- the only key a node
// has any reason to trust about this.
func TestAReportedHeadIsRecordedAndReceipted(t *testing.T) {
	l := newLab(t)
	dir := l.withAnchors(t)
	node := l.enrolled(t, "web1.example")
	ctx := context.Background()

	res, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 3, Hash: headA})
	if err != nil {
		t.Fatal(err)
	}
	if res.NodeID != "web1.example" || res.Seq != 3 || res.Hash != headA {
		t.Fatalf("the receipt is for %s %d %s", res.NodeID, res.Seq, res.Hash)
	}
	if err := nodeevidence.VerifyReceipt(l.ca.Cert, res.NodeID, res.Seq, res.Hash, res.Received, res.Signature); err != nil {
		t.Fatalf("the receipt does not verify against the lab's CA: %v", err)
	}
	lines := anchorLines(t, dir, "web1.example")
	if len(lines) != 1 || lines[0].Result != nodeevidence.AnchorAccepted ||
		lines[0].Seq != 3 || lines[0].Hash != headA || lines[0].Receipt != res.Signature ||
		lines[0].Received != res.Received {
		t.Fatalf("the stored line does not match the receipt: %+v", lines)
	}
	info, err := os.Stat(filepath.Join(dir, "web1.example.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 && runtime.GOOS != "windows" {
		t.Errorf("the anchor file is mode %o; it holds a security record and is the hub's alone", mode)
	}

	// The same head again is the reconnect case: accepted, the same
	// receipt, and no second line.
	again, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 3, Hash: headA})
	if err != nil {
		t.Fatalf("the same head reported twice was refused: %v", err)
	}
	if again.Signature != res.Signature || again.Received != res.Received {
		t.Error("a repeated head was given a different receipt from the first")
	}
	if got := len(anchorLines(t, dir, "web1.example")); got != 1 {
		t.Errorf("a repeated head added a line; there are %d", got)
	}

	// A higher one is accepted after it.
	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 9, Hash: headB}); err != nil {
		t.Fatal(err)
	}
	if got := len(anchorLines(t, dir, "web1.example")); got != 2 {
		t.Errorf("there are %d lines after a second head", got)
	}
}

// A head that contradicts the record is a conflict: 409, no receipt, a
// line saying what it contradicted, an event and a count. Both shapes --
// the same number with another hash, and a number gone backwards.
func TestAContradictingHeadIsAConflict(t *testing.T) {
	l := newLab(t).withEvents(t)
	dir := l.withAnchors(t)
	node := l.enrolled(t, "web1.example")
	ctx := context.Background()

	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 3, Hash: headA}); err != nil {
		t.Fatal(err)
	}
	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 9, Hash: headA}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		seq       uint64
		priorSeq  uint64
		priorHash string
	}{
		{"the same number, another hash", 9, 9, headA},
		// Below the highest, at a number the hub accepted: the
		// contradiction named is the hash it had there.
		{"a number gone backwards", 3, 3, headA},
		// Below the highest, at a number the hub never saw: the
		// contradiction is the highest.
		{"a number gone backwards to one never reported", 5, 9, headA},
	}
	for i, c := range cases {
		_, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: c.seq, Hash: headB})
		if err == nil {
			t.Fatalf("%s: accepted", c.name)
		}
		status, code := refusedStatus(t, err)
		if status != http.StatusConflict || code != transport.CodeEvidenceConflict {
			t.Errorf("%s: answered %d %q", c.name, status, code)
		}
		lines := anchorLines(t, dir, "web1.example")
		if len(lines) != 3+i {
			t.Fatalf("%s: %d lines", c.name, len(lines))
		}
		last := lines[len(lines)-1]
		if last.Result != nodeevidence.AnchorConflict || last.Seq != c.seq || last.Hash != headB ||
			last.PriorSeq != c.priorSeq || last.PriorHash != c.priorHash || last.Receipt != "" {
			t.Errorf("%s: the conflict line is %+v", c.name, last)
		}
	}

	events, _, err := l.server.Events.Read(eventbus.Earliest,
		[]string{"halite/node/web1.example/evidence/conflict"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(cases) {
		t.Fatalf("%d conflict events for %d conflicts", len(events), len(cases))
	}
	if events[0].Data["prior_hash"] != headA || events[0].Data["hash"] != headB {
		t.Errorf("the event does not carry both hashes: %v", events[0].Data)
	}

	// The conflicts did not move the accepted head: the next report
	// above it is accepted as though they had not happened.
	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 10, Hash: headB}); err != nil {
		t.Fatalf("a head above the accepted one was refused after conflicts: %v", err)
	}

	var out bytes.Buffer
	if err := l.server.Metrics.Write(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `halite_hub_evidence_anchors_total{result="conflict"} 3`) {
		t.Errorf("the conflicts are not counted:\n%s", out.String())
	}
}

// The identity is the certificate's. A body naming another node is
// refused, and nothing is written under either name.
func TestAnAnchorIsFiledUnderTheCertificatesName(t *testing.T) {
	l := newLab(t)
	dir := l.withAnchors(t)
	web := l.enrolled(t, "web1.example")
	l.enrolled(t, "db1.example")
	ctx := context.Background()

	_, err := web.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{NodeID: "db1.example", Seq: 1, Hash: headA})
	if err == nil {
		t.Fatal("a node anchored a head under another node's name")
	}
	if status, _ := refusedStatus(t, err); status != http.StatusForbidden {
		t.Errorf("answered %d", status)
	}
	if lines := anchorLines(t, dir, "db1.example"); len(lines) != 0 {
		t.Errorf("the other node's record was written: %+v", lines)
	}
	if lines := anchorLines(t, dir, "web1.example"); len(lines) != 0 {
		t.Errorf("the refused request was recorded anyway: %+v", lines)
	}

	// The same report naming itself, or nobody, lands under its own name.
	if _, err := web.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{NodeID: "web1.example", Seq: 1, Hash: headA}); err != nil {
		t.Fatal(err)
	}
	if lines := anchorLines(t, dir, "web1.example"); len(lines) != 1 {
		t.Errorf("web1.example has %d lines", len(lines))
	}
	if lines := anchorLines(t, dir, "db1.example"); len(lines) != 0 {
		t.Errorf("db1.example has %d lines", len(lines))
	}
}

// A head that is not one is refused before anything is written or
// signed.
func TestAMalformedHeadIsRefused(t *testing.T) {
	l := newLab(t)
	dir := l.withAnchors(t)
	node := l.enrolled(t, "web1.example")
	for _, req := range []transport.EvidenceAnchorRequest{
		{Seq: 0, Hash: headA},
		{Seq: 1, Hash: "sha256:abc"},
		{Seq: 1, Hash: headA + "\nreceived 1999-01-01T00:00:00Z"},
	} {
		_, err := node.AnchorEvidence(context.Background(), req)
		if err == nil {
			t.Errorf("%+v was accepted", req)
			continue
		}
		if status, _ := refusedStatus(t, err); status != http.StatusBadRequest {
			t.Errorf("%+v: answered %d", req, status)
		}
	}
	if lines := anchorLines(t, dir, "web1.example"); len(lines) != 0 {
		t.Errorf("malformed heads were recorded: %+v", lines)
	}
}

// The store remembers across a restart: the highest head is read back
// from the file, so a hub that restarts does not accept a rolled-back
// chain as new.
func TestTheAnchorStoreRemembersAcrossARestart(t *testing.T) {
	l := newLab(t)
	dir := l.withAnchors(t)
	node := l.enrolled(t, "web1.example")
	ctx := context.Background()
	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 9, Hash: headA}); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenAnchorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.server.Anchors = fresh
	_, err = node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 4, Hash: headB})
	if err == nil {
		t.Fatal("after a restart, a head below the recorded one was accepted")
	}
	if status, _ := refusedStatus(t, err); status != http.StatusConflict {
		t.Errorf("answered %d", status)
	}
}

// What the comment on tagEvidenceConflict says, held: a node can put an
// event on exactly that tag itself, which is why the event is a prompt
// and the anchor file is the finding.
func TestANodeCanWriteTheConflictTagItself(t *testing.T) {
	got, err := nodeEventTag("web1.example", "evidence/conflict")
	if err != nil {
		t.Fatal(err)
	}
	if got != tagEvidenceConflict("web1.example") {
		t.Errorf("a node sending evidence/conflict lands on %q, not %q; the comment on "+
			"tagEvidenceConflict is wrong", got, tagEvidenceConflict("web1.example"))
	}
}

// Moving a node's file aside starts its record again, on a running hub:
// what docs/operations.md tells an operator to do once a node's chain
// has legitimately restarted -- a snapshot restore, a wiped state
// directory -- and which a hub that remembered the old file's highest
// head would turn into a conflict on every report.
func TestMovingAnAnchorFileAsideStartsTheRecordAgain(t *testing.T) {
	l := newLab(t)
	dir := l.withAnchors(t)
	node := l.enrolled(t, "web1.example")
	ctx := context.Background()
	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 9, Hash: headA}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "web1.example.jsonl")
	if err := os.Rename(path, path+".before-restore"); err != nil {
		t.Fatal(err)
	}
	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 2, Hash: headB}); err != nil {
		t.Fatalf("a restarted chain was refused after its old record was moved aside: %v", err)
	}
	lines := anchorLines(t, dir, "web1.example")
	if len(lines) != 1 || lines[0].Result != nodeevidence.AnchorAccepted || lines[0].Seq != 2 {
		t.Errorf("the new file holds %+v", lines)
	}
}

// SubscribeOpened says the stream is up once the hub has accepted it,
// before the first message is read, and never for a stream the hub
// refused. The node reports its evidence head from this, so "connected"
// has to mean the hub said yes.
func TestSubscribeOpenedFiresOnAcceptanceOnly(t *testing.T) {
	l := newLab(t)
	client := l.enrolled(t, "web1.example")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	refused := 0
	if err := client.SubscribeOpened(ctx, transport.SubscribeRequest{NodeID: "db1.example"},
		func() { refused++ }, func(transport.Message) error { return nil }); err == nil {
		t.Fatal("a stream under another node's name was accepted")
	}
	if refused != 0 {
		t.Error("opened was called for a stream the hub refused")
	}

	opened := make(chan struct{})
	messages := 0
	errs := make(chan error, 1)
	go func() {
		errs <- client.SubscribeOpened(ctx, transport.SubscribeRequest{NodeID: "web1.example"},
			func() {
				if messages != 0 {
					t.Error("opened was called after a message had been read")
				}
				close(opened)
			},
			func(transport.Message) error {
				messages++
				return nil
			})
	}()
	select {
	case <-opened:
	case err := <-errs:
		t.Fatalf("the stream ended before it was reported open: %v", err)
	case <-ctx.Done():
		t.Fatal("opened was never called for an accepted stream")
	}
	cancel()
	<-errs
}

// A node is held to a rate, as a token bucket, and a report over it is
// refused before it costs anything: 429, no line written, nothing
// signed, counted. Without it a compromised node could report an
// ever-larger head as fast as the hub would fsync it, growing its own
// file without bound. Another node's bucket is its own, and the refused
// node is let back in as the bucket refills. DIVERGENCE 5.231.
func TestANodeOverItsAnchorRateIsRefusedAndNothingIsWritten(t *testing.T) {
	l := newLab(t)
	dir := l.withAnchors(t)
	now := time.Now()
	l.server.Now = func() time.Time { return now }
	l.server.Anchors.Rate, l.server.Anchors.Burst = 0.5, 3
	web1 := l.enrolled(t, "web1.example")
	web2 := l.enrolled(t, "web2.example")
	ctx := context.Background()

	for seq := uint64(1); seq <= 3; seq++ {
		if _, err := web1.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: seq, Hash: headA}); err != nil {
			t.Fatalf("report %d of a burst of 3 was refused: %v", seq, err)
		}
	}
	_, err := web1.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 4, Hash: headA})
	if err == nil {
		t.Fatal("a fourth report inside a burst of 3 was accepted")
	}
	// A StatusError rather than a RefusedError: 429 is "not now", and the
	// client keeps the two apart so that only a refusal is treated as
	// final. The code is what the node acts on.
	var status *transport.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusTooManyRequests ||
		transport.CodeOf(err) != transport.CodeRateLimited {
		t.Errorf("the refusal was %v (code %q)", err, transport.CodeOf(err))
	}
	if lines := anchorLines(t, dir, "web1.example"); len(lines) != 3 {
		t.Errorf("a refused report left a line: %d lines", len(lines))
	}

	if _, err := web2.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 1, Hash: headA}); err != nil {
		t.Errorf("another node was held to web1's rate: %v", err)
	}

	now = now.Add(2 * time.Second) // one token at 0.5 a second
	if _, err := web1.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 4, Hash: headA}); err != nil {
		t.Errorf("the bucket refilled and the report was still refused: %v", err)
	}

	var out bytes.Buffer
	if err := l.server.Metrics.Write(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `halite_hub_evidence_anchors_total{result="rate_limited"} 1`) {
		t.Errorf("the refusal is not counted:\n%s", out.String())
	}
}

// restartAnchors replaces the lab's store with a fresh one over the same
// directory, which is what a hub restarting is to the store, and
// collects what it warns.
func (l *lab) restartAnchors(t *testing.T, dir string) *[]string {
	t.Helper()
	fresh, err := OpenAnchorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var warned []string
	fresh.Warn = func(msg string, _ ...any) { warned = append(warned, msg) }
	l.server.Anchors = fresh
	return &warned
}

// A hub that stopped part-way through appending leaves a last line with
// no newline, and no receipt was sent for it. The next report after the
// restart is answered, the fragment is gone, and the hub says so. Before
// DIVERGENCE 5.234 every report from the node failed on the fragment
// until somebody edited the file by hand.
func TestATornLastAnchorLineIsDroppedOnLoad(t *testing.T) {
	l := newLab(t)
	dir := l.withAnchors(t)
	node := l.enrolled(t, "web1.example")
	ctx := context.Background()
	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 9, Hash: headA}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "web1.example.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":12,"hash":"sha256:fedc`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	warned := l.restartAnchors(t, dir)
	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 12, Hash: headB}); err != nil {
		t.Fatalf("the first report after a torn append was refused: %v", err)
	}
	lines := anchorLines(t, dir, "web1.example")
	if len(lines) != 2 || lines[0].Seq != 9 || lines[1].Seq != 12 ||
		lines[1].Result != nodeevidence.AnchorAccepted {
		t.Fatalf("after the repair the file holds %+v", lines)
	}
	if len(*warned) != 1 || !strings.Contains((*warned)[0], "incomplete") {
		t.Errorf("the repair was not said, or said wrongly: %q", *warned)
	}
}

// A last line that is whole and lacks only its newline is a head the
// node really reported, and is kept: the next report of the same head
// is the idempotent repeat, answered with the receipt on that line, and
// a lower one is a conflict against it.
func TestAWholeLastAnchorLineMissingItsNewlineIsKept(t *testing.T) {
	l := newLab(t)
	dir := l.withAnchors(t)
	node := l.enrolled(t, "web1.example")
	ctx := context.Background()
	first, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 9, Hash: headA})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "web1.example.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.TrimSuffix(raw, []byte("\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	warned := l.restartAnchors(t, dir)
	again, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 9, Hash: headA})
	if err != nil {
		t.Fatalf("the same head after the repair was refused: %v", err)
	}
	if again.Signature != first.Signature {
		t.Error("the kept line's receipt was not the one answered; the line was not kept")
	}
	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 4, Hash: headB}); err == nil {
		t.Error("a lower head was accepted, so the kept line is not the record's top")
	}
	repaired, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(repaired, raw) {
		t.Errorf("the kept line was not terminated in place:\n%s", repaired)
	}
	if len(*warned) != 1 || !strings.Contains((*warned)[0], "kept") {
		t.Errorf("the repair was not said, or said wrongly: %q", *warned)
	}
}

// Only the shape a crash leaves is repaired. A broken line that ends in
// a newline is damage or an edit, and the node's reports are still
// refused until somebody looks.
func TestABrokenAnchorLineInTheMiddleIsStillRefused(t *testing.T) {
	l := newLab(t)
	dir := l.withAnchors(t)
	node := l.enrolled(t, "web1.example")
	ctx := context.Background()
	path := filepath.Join(dir, "web1.example.jsonl")
	if err := os.WriteFile(path, []byte("{\"seq\":9,\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l.restartAnchors(t, dir)
	_, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 12, Hash: headB})
	if err == nil {
		t.Fatal("a report was accepted over a broken line")
	}
	if !strings.Contains(err.Error(), "could not record this head") {
		t.Errorf("refused with %v", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "{\"seq\":9,\n" {
		t.Errorf("the broken file was changed: %q", raw)
	}
}

// An append that fails part-way on a running hub -- a full disk -- is
// cut back. The hub only repairs a file when it first reads it, so
// without this the next report would be appended after the fragment and
// the file would hold a broken line in the middle for good.
func TestAFailedAnchorAppendIsCutBack(t *testing.T) {
	l := newLab(t)
	dir := l.withAnchors(t)
	node := l.enrolled(t, "web1.example")
	ctx := context.Background()
	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 9, Hash: headA}); err != nil {
		t.Fatal(err)
	}

	real := appendWrite
	appendWrite = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2])
		return n, errors.New("no space left on device")
	}
	_, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 10, Hash: headB})
	appendWrite = real
	if err == nil {
		t.Fatal("a report whose append failed was answered as accepted")
	}

	if _, err := node.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{Seq: 11, Hash: headB}); err != nil {
		t.Fatalf("the report after a failed append was refused: %v", err)
	}
	lines := anchorLines(t, dir, "web1.example")
	if len(lines) != 2 || lines[0].Seq != 9 || lines[1].Seq != 11 {
		t.Fatalf("after a failed append the file holds %+v", lines)
	}
}

// The first conflict must be visible to increase(). A labelled counter
// has no series until it is incremented, so one that is born at 1 gives
// Prometheus no earlier sample to subtract and the alert in
// docs/metrics.md stays silent for exactly the report it exists for. A
// hub that has seen no report at all exports every result at 0.
func TestEveryAnchorResultIsExportedAtZeroBeforeAnyReport(t *testing.T) {
	l := newLab(t)
	l.withAnchors(t)
	// Metrics are declared on first use, and a scrape gets there by
	// counting its own authorization decision before it writes.
	l.server.m()
	var out bytes.Buffer
	if err := l.server.Metrics.Write(&out); err != nil {
		t.Fatal(err)
	}
	for _, result := range []string{"accepted", "conflict", "rate_limited", "failed"} {
		want := `halite_hub_evidence_anchors_total{result="` + result + `"} 0`
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %s before the first report:\n%s", want, out.String())
		}
	}
}
