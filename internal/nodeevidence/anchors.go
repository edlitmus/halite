package nodeevidence

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
)

// The results a hub records for a reported head.
const (
	// AnchorAccepted is a head the hub took: numbered above everything
	// it had recorded for the node, or the same head again.
	AnchorAccepted = "accepted"
	// AnchorConflict is a head that contradicts what the hub had: a
	// number it had recorded with a different hash, or a number below
	// the highest it had. Either way the node's chain is no longer the
	// chain it reported, which is the event the anchor exists to catch.
	AnchorConflict = "conflict"
)

// Anchor is one line of the hub's record of a node's heads, kept at
// `<state_dir>/evidence-anchors/<node id>.jsonl`.
//
// Declared here rather than in the hub because two programs read it --
// the hub writes it and `halite-node verify-evidence --anchors` checks a
// chain against it -- and a line format declared twice is a pair that
// will one day disagree. Every field after Result carries `omitempty`,
// for the reason Record's later fields do: a line an older hub wrote must
// read the same under a newer build.
type Anchor struct {
	Seq  uint64 `json:"seq"`
	Hash string `json:"hash"`
	// Received is when the hub took it, in the spelling ReceivedNow
	// writes, which is also the spelling the receipt signed.
	Received string `json:"received"`
	Result   string `json:"result"`
	// Receipt is the signature the hub answered with, for an accepted
	// head. Kept so that the line and the receipt the node filed can be
	// matched, not because anything trusts it from here: the hub holds
	// the key that made it.
	Receipt string `json:"receipt,omitempty"`
	// PriorSeq and PriorHash are what the hub had that this head
	// contradicts, on a conflict. PriorSeq is the record the comparison
	// was against: the same number with a different hash, or the
	// highest number when the head went backwards and the hub had
	// nothing at that number.
	PriorSeq  uint64 `json:"prior_seq,omitempty"`
	PriorHash string `json:"prior_hash,omitempty"`

	// Line is where it was read from, for a message. Not part of the
	// format.
	Line int `json:"-"`
}

// ReadAnchors parses a hub's anchor file.
//
// A line that does not parse, or names a result this build does not
// know, stops the read rather than being skipped. The file is the thing
// being checked against; a verifier that quietly dropped the line it
// could not read would be checking against less than it was given and
// saying nothing about it.
func ReadAnchors(r io.Reader) ([]Anchor, error) {
	reader := bufio.NewReader(r)
	var out []Anchor
	lineNo := 0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				var a Anchor
				if err := json.Unmarshal(trimmed, &a); err != nil {
					if line[len(line)-1] != '\n' {
						// The one shape a hub stopping mid-append leaves.
						// Still refused here: this reads a file as it was
						// handed over, and the hub, which sent no receipt
						// for the line, is what removes it.
						return nil, fmt.Errorf("line %d is incomplete: a hub stopped while writing it and "+
							"sent no receipt for it. The hub removes it the next time this node reports; "+
							"take a copy of the file after that", lineNo)
					}
					return nil, fmt.Errorf("line %d is not an anchor record: %w", lineNo, err)
				}
				switch a.Result {
				case AnchorAccepted, AnchorConflict:
				default:
					return nil, fmt.Errorf("line %d records a result of %q, which this build does not know", lineNo, a.Result)
				}
				a.Line = lineNo
				out = append(out, a)
			}
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// AnchorCheck is what checking a chain against a hub's anchors and its
// own receipts found.
type AnchorCheck struct {
	// Anchored is how many accepted heads were compared with the chain,
	// Receipts how many receipt records were checked, and Found how many
	// the chain holds -- which differs from Receipts when there was no
	// certificate to check them with.
	Anchored int
	Receipts int
	Found    int
	// Conflicts are the lines on which the hub itself recorded a
	// contradiction. A finding even when every accepted line matches:
	// the hub saw this node's chain go backwards or change, and the
	// chain in front of the verifier may simply be the one it went back
	// to.
	Conflicts []Anchor
	// Unchecked says what could not be compared and why -- a head older
	// than the oldest record still here, which is what archiving sealed
	// segments produces. Not a break, and not silence either.
	Unchecked []string
	Breaks    []Break
}

// OK reports whether the chain agrees with the hub and with its own
// receipts.
func (c *AnchorCheck) OK() bool { return len(c.Breaks) == 0 && len(c.Conflicts) == 0 }

// CheckAnchors compares the chain in dir with the anchors a hub recorded
// for it, and checks every anchor receipt the chain holds against the
// enrollment CA certificate.
//
// The two halves face opposite ways. The anchors are the hub's account,
// and a chain that disagrees with them was rewritten after it was
// reported -- the attack Verify alone cannot see, because a chain
// recomputed from its first record verifies perfectly. The receipts are
// the node's account of what the hub acknowledged, and a valid one the
// hub's file does not contain is a hub that has lost or removed
// something it signed.
//
// anchorsName names the anchor file in a break, and is empty when the
// caller has no anchor file: the receipts are then checked against the
// CA and the chain, and not against a hub record nobody supplied. ca may
// be nil when no
// certificate could be read; any receipt is then a break, because a
// receipt nobody checked is not one that held.
func CheckAnchors(dir string, anchorsName string, anchors []Anchor, ca *x509.Certificate) (*AnchorCheck, error) {
	type receipt struct {
		segment string
		line    int
		rec     Record
	}
	var receipts []receipt
	var first, last uint64
	seen := false

	segments, err := Segments(dir)
	if err != nil {
		return nil, err
	}
	// The first pass finds the receipts and the chain's extent. A
	// receipt names a record before it, so the hashes it needs are
	// already behind the reader by the time it is found; a second pass
	// collects exactly the hashes the comparisons need rather than
	// holding every hash in a chain that may be years long.
	for _, segment := range segments {
		if _, err := scanLines(segment, func(lineNo int, line []byte) error {
			var r Record
			if json.Unmarshal(line, &r) != nil {
				return nil // Verify's to report.
			}
			if !seen || r.Seq < first {
				first = r.Seq
			}
			if r.Seq > last {
				last = r.Seq
			}
			seen = true
			if r.Kind == KindAnchorReceipt {
				receipts = append(receipts, receipt{segment, lineNo, r})
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	need := map[uint64]bool{}
	type head struct {
		seq  uint64
		hash string
	}
	hubHas := map[head]bool{}
	for _, a := range anchors {
		if a.Result == AnchorAccepted {
			need[a.Seq] = true
			hubHas[head{a.Seq, a.Hash}] = true
		}
	}
	for _, rc := range receipts {
		if seq, err := strconv.ParseUint(rc.rec.Detail[ReceiptAnchoredSeq], 10, 64); err == nil {
			need[seq] = true
		}
	}
	hashes := map[uint64]string{}
	for _, segment := range segments {
		if _, err := scanLines(segment, func(_ int, line []byte) error {
			var r Record
			if json.Unmarshal(line, &r) != nil {
				return nil
			}
			// The first record at a number wins. A chain holding two is
			// already broken and Verify says so; this compares against
			// the one a reader meets first, and says which.
			if need[r.Seq] {
				if _, dup := hashes[r.Seq]; !dup {
					hashes[r.Seq] = r.Hash
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	res := &AnchorCheck{Found: len(receipts)}
	// where says what the chain holds at a number, or why it holds
	// nothing there, so that the two halves word the same fact the
	// same way.
	where := func(seq uint64) (hash string, missing string, archived bool) {
		if h, ok := hashes[seq]; ok {
			return h, "", false
		}
		switch {
		case !seen:
			return "", "this chain holds no records at all", false
		case seq > last:
			return "", fmt.Sprintf("this chain ends at record %d", last), false
		case seq < first:
			return "", fmt.Sprintf("this chain begins at record %d", first), true
		default:
			return "", fmt.Sprintf("this chain has no record %d although it runs from %d to %d", seq, first, last), false
		}
	}

	for _, a := range anchors {
		if a.Result == AnchorConflict {
			res.Conflicts = append(res.Conflicts, a)
			continue
		}
		res.Anchored++
		hash, missing, archived := where(a.Seq)
		switch {
		case archived:
			res.Unchecked = append(res.Unchecked, fmt.Sprintf(
				"the hub's line %d anchors record %d, and %s: those records were archived, or removed",
				a.Line, a.Seq, missing))
		case missing != "":
			res.Breaks = append(res.Breaks, Break{
				Segment: anchorsName, Line: a.Line, Seq: a.Seq,
				Reason: fmt.Sprintf("the hub recorded record %d as %s and %s, so records the hub was told about are gone",
					a.Seq, a.Hash, missing),
			})
		case hash != a.Hash:
			res.Breaks = append(res.Breaks, Break{
				Segment: anchorsName, Line: a.Line, Seq: a.Seq,
				Reason: fmt.Sprintf("the hub recorded record %d as %s and this chain's record %d is %s, "+
					"so the chain has been rewritten since it was reported", a.Seq, a.Hash, a.Seq, hash),
			})
		}
	}

	if len(receipts) > 0 && ca == nil {
		res.Breaks = append(res.Breaks, Break{
			Segment: receipts[0].segment, Line: receipts[0].line, Seq: receipts[0].rec.Seq,
			Reason: fmt.Sprintf("this chain holds %d anchor receipt(s) and there is no CA certificate to check them against",
				len(receipts)),
		})
		return res, nil
	}
	for _, rc := range receipts {
		res.Receipts++
		d := rc.rec.Detail
		fail := func(reason string) {
			res.Breaks = append(res.Breaks, Break{
				Segment: rc.segment, Line: rc.line, Seq: rc.rec.Seq, Reason: reason,
			})
		}
		seq, err := strconv.ParseUint(d[ReceiptAnchoredSeq], 10, 64)
		if err != nil {
			fail(fmt.Sprintf("this anchor receipt's %s is %q, which is not a record number", ReceiptAnchoredSeq, d[ReceiptAnchoredSeq]))
			continue
		}
		if err := VerifyReceipt(ca, d[ReceiptNode], seq, d[ReceiptAnchoredHash], d[ReceiptReceived], d[ReceiptSignature]); err != nil {
			fail("this anchor receipt does not hold: " + err.Error())
			continue
		}
		if rc.rec.NodeID != "" && d[ReceiptNode] != rc.rec.NodeID {
			fail(fmt.Sprintf("this anchor receipt was signed for %s and is filed in the chain of %s",
				d[ReceiptNode], rc.rec.NodeID))
			continue
		}
		// The receipt is genuine, so the hub said this. Its file not
		// saying it too is the hub's half of the contradiction: a line
		// lost or removed after it was signed for. Only when there is a
		// file to compare with; without --anchors there is nothing to
		// contradict.
		if anchorsName != "" && !hubHas[head{seq, d[ReceiptAnchoredHash]}] {
			fail(fmt.Sprintf("the hub signed a receipt for record %d as %s and %s has no accepted line for it, "+
				"so the hub's record has lost or removed an anchor it acknowledged",
				seq, d[ReceiptAnchoredHash], filepath.Base(anchorsName)))
		}
		hash, missing, archived := where(seq)
		switch {
		case archived:
			res.Unchecked = append(res.Unchecked, fmt.Sprintf(
				"record %d is a receipt for record %d, and %s", rc.rec.Seq, seq, missing))
		case missing != "":
			fail(fmt.Sprintf("this receipt is for record %d and %s", seq, missing))
		case hash != d[ReceiptAnchoredHash]:
			fail(fmt.Sprintf("the hub signed record %d as %s and this chain's record %d is %s, "+
				"so the chain has been rewritten since the hub acknowledged it", seq, d[ReceiptAnchoredHash], seq, hash))
		}
	}
	sort.SliceStable(res.Breaks, func(i, j int) bool { return res.Breaks[i].Seq < res.Breaks[j].Seq })
	return res, nil
}
