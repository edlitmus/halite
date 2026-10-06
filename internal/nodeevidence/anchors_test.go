package nodeevidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// anchoredChain writes a chain the way an agent does with a hub that
// takes its reports: two records, a report, a receipt, two more records,
// a report, a receipt. It returns the directory and the hub's anchor
// lines for it.
//
// The hub's half is made with the same SignReceipt the hub uses, so a
// disagreement between this and the hub would be a disagreement in one
// function, not two.
func anchoredChain(t *testing.T, ca *pki.CA) (string, []Anchor) {
	t.Helper()
	dir := t.TempDir()
	log, err := Open(dir, Options{NodeID: "web1.example"})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var anchors []Anchor
	report := func() {
		seq, hash := log.Head()
		received := ReceivedNow(time.Now())
		sig, err := SignReceipt(ca.Key, "web1.example", seq, hash, received)
		if err != nil {
			t.Fatal(err)
		}
		anchors = append(anchors, Anchor{
			Seq: seq, Hash: hash, Received: received, Result: AnchorAccepted, Receipt: sig,
			Line: len(anchors) + 1,
		})
		if err := log.Append(KindAnchorReceipt, map[string]string{
			ReceiptAnchoredSeq:  strconv.FormatUint(seq, 10),
			ReceiptAnchoredHash: hash,
			ReceiptNode:         "web1.example",
			ReceiptReceived:     received,
			ReceiptSignature:    sig,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendJob := func(jid string) {
		if err := log.Append(KindJobAccepted, map[string]string{"jid": jid, "fun": "test.ping"}); err != nil {
			t.Fatal(err)
		}
		if err := log.Append(KindJobResult, map[string]string{"jid": jid, "success": "true"}); err != nil {
			t.Fatal(err)
		}
	}
	appendJob("20261006T120000000001")
	report()
	appendJob("20261006T120000000002")
	report()
	return dir, anchors
}

// readChain and writeChain let a test be the attacker: anything with root
// on the node can read the chain, change what it likes, and write a new
// one.
func readChain(t *testing.T, dir string) []Record {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, currentName))
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// forge recomputes every hash and link from the first record, which is
// the attack a hash chain cannot detect on its own: the result verifies
// perfectly.
func forge(t *testing.T, dir string, records []Record) {
	t.Helper()
	var b strings.Builder
	prev := ""
	for i, r := range records {
		r.Seq = uint64(i + 1)
		r.Prev = prev
		hash, err := hashOf(r)
		if err != nil {
			t.Fatal(err)
		}
		r.Hash = hash
		prev = hash
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, currentName), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Verify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("the forgery does not verify on its own, so this test is not testing the attack: %v", res.Breaks)
	}
}

func breaksText(c *AnchorCheck) string {
	var parts []string
	for _, b := range c.Breaks {
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n")
}

// The control: a chain and the hub's record of it agree, and every
// receipt holds.
func TestAChainAgreesWithTheHubThatAnchoredIt(t *testing.T) {
	ca := testCA(t)
	dir, anchors := anchoredChain(t, ca)
	check, err := CheckAnchors(dir, "web1.example.jsonl", anchors, ca.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if !check.OK() {
		t.Fatalf("an honest chain disagrees with its anchors:\n%s", breaksText(check))
	}
	if check.Anchored != 2 || check.Receipts != 2 {
		t.Errorf("checked %d anchors and %d receipts, expected 2 and 2", check.Anchored, check.Receipts)
	}
}

// The attack the anchor exists for. The node's chain is recomputed from
// its first record with one job's result changed -- and with the receipts
// removed, because an attacker who can rewrite the chain can drop them
// too. The chain verifies on its own; against the hub's record it does
// not.
func TestARewrittenChainContradictsTheHub(t *testing.T) {
	ca := testCA(t)
	dir, anchors := anchoredChain(t, ca)

	var kept []Record
	for _, r := range readChain(t, dir) {
		if r.Kind == KindAnchorReceipt {
			continue
		}
		if r.Kind == KindJobResult && r.Detail["jid"] == "20261006T120000000001" {
			r.Detail["success"] = "false"
		}
		kept = append(kept, r)
	}
	forge(t, dir, kept)

	check, err := CheckAnchors(dir, "web1.example.jsonl", anchors, ca.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if check.OK() {
		t.Fatal("a chain rewritten after it was anchored agrees with the hub")
	}
	text := breaksText(check)
	if !strings.Contains(text, "rewritten since it was reported") {
		t.Errorf("the break does not say the chain was rewritten:\n%s", text)
	}
	// Both anchors: the first because record 2 changed, and the second
	// because removing the receipt renumbered everything after it.
	if len(check.Breaks) != 2 {
		t.Errorf("expected a break per anchor, got:\n%s", text)
	}
}

// Truncation is the cheapest rewrite of all, and leaves a chain that
// verifies: the records after the cut are simply gone.
func TestATruncatedChainContradictsTheHub(t *testing.T) {
	ca := testCA(t)
	dir, anchors := anchoredChain(t, ca)
	forge(t, dir, readChain(t, dir)[:2])

	check, err := CheckAnchors(dir, "web1.example.jsonl", anchors, ca.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(breaksText(check), "this chain ends at record 2") {
		t.Errorf("truncation is not reported as records the hub was told about being gone:\n%s",
			breaksText(check))
	}
}

// A receipt that has been tampered with is a break, even with no anchor
// file: the receipts are the node's half and are always checked. The
// chain is re-forged around the change so that only the receipt check
// can find it.
func TestATamperedReceiptIsABreak(t *testing.T) {
	ca := testCA(t)
	dir, _ := anchoredChain(t, ca)
	records := readChain(t, dir)
	for i := range records {
		if records[i].Kind == KindAnchorReceipt {
			records[i].Detail[ReceiptAnchoredSeq] = "1"
			break
		}
	}
	forge(t, dir, records)

	check, err := CheckAnchors(dir, "", nil, ca.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(breaksText(check), "does not hold") {
		t.Errorf("a receipt whose head was changed still holds:\n%s", breaksText(check))
	}
}

// A receipt that holds names a hash, and the chain no longer having that
// hash at that number is a break, even with no anchor file.
func TestAReceiptForARecordTheChainNoLongerHasIsABreak(t *testing.T) {
	ca := testCA(t)
	dir, _ := anchoredChain(t, ca)
	records := readChain(t, dir)
	records[0].Detail["fun"] = "cmd.run"
	forge(t, dir, records)

	check, err := CheckAnchors(dir, "", nil, ca.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(breaksText(check), "since the hub acknowledged it") {
		t.Errorf("a receipt for a record that has since changed is not a break:\n%s", breaksText(check))
	}
}

// The hub's half of the contradiction: it signed a receipt and its file
// no longer holds the line.
func TestAHubThatDropsAnAnchorItSignedIsContradicted(t *testing.T) {
	ca := testCA(t)
	dir, anchors := anchoredChain(t, ca)
	check, err := CheckAnchors(dir, "web1.example.jsonl", anchors[1:], ca.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(breaksText(check), "lost or removed an anchor it acknowledged") {
		t.Errorf("a receipt the hub's file does not hold is not a break:\n%s", breaksText(check))
	}
}

// A conflict the hub recorded is a finding even when every accepted line
// matches the chain in front of the verifier.
func TestAConflictTheHubRecordedIsAFinding(t *testing.T) {
	ca := testCA(t)
	dir, anchors := anchoredChain(t, ca)
	anchors = append(anchors, Anchor{
		Seq: 1, Hash: otherHash, Received: ReceivedNow(time.Now()), Result: AnchorConflict,
		PriorSeq: 1, PriorHash: someHash, Line: 3,
	})
	check, err := CheckAnchors(dir, "web1.example.jsonl", anchors, ca.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Breaks) != 0 {
		t.Fatalf("unexpected breaks:\n%s", breaksText(check))
	}
	if check.OK() || len(check.Conflicts) != 1 {
		t.Errorf("a recorded conflict was not reported: ok=%v conflicts=%d", check.OK(), len(check.Conflicts))
	}
}

// A chain with receipts and no certificate to check them against is not
// a chain whose receipts held.
func TestReceiptsWithNoCertificateAreABreak(t *testing.T) {
	ca := testCA(t)
	dir, _ := anchoredChain(t, ca)
	check, err := CheckAnchors(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if check.OK() || !strings.Contains(breaksText(check), "no CA certificate") {
		t.Errorf("receipts nobody could check were passed:\n%s", breaksText(check))
	}
}

// The file is read strictly: a line that does not parse, or a result
// nobody defined, stops the read rather than quietly shrinking what the
// chain is checked against.
func TestAnAnchorFileIsReadStrictly(t *testing.T) {
	good := `{"seq":1,"hash":"` + someHash + `","received":"2026-10-06T12:00:00Z","result":"accepted","receipt":"x"}` + "\n"
	got, err := ReadAnchors(strings.NewReader(good))
	if err != nil || len(got) != 1 || got[0].Line != 1 {
		t.Fatalf("a good file: %v %v", got, err)
	}
	for name, body := range map[string]string{
		"a broken line":     good + "{\"seq\":2,\n",
		"an unknown result": good + `{"seq":2,"hash":"` + someHash + `","received":"2026-10-06T12:00:00Z","result":"maybe"}` + "\n",
	} {
		if _, err := ReadAnchors(strings.NewReader(body)); err == nil {
			t.Errorf("%s was read without complaint", name)
		}
	}
}
