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
