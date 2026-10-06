package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/edlitmus/halite/internal/nodeevidence"
	"github.com/edlitmus/halite/internal/transport"
)

// SPEC 25.7's anchor, from the node's side: report the head of the
// evidence chain to the hub when the stream comes up and after every
// job, and file the hub's signed receipt in the chain.
//
// # Why the hub, and why both directions
//
// A chain on a node detects editing and not rewriting. Anything with root
// here can recompute it from the first record and produce a forgery that
// verifies, so the chain is evidence against a compromised hub and
// nothing at all against a compromised node. A copy of the head somewhere
// the node cannot write changes that for everything up to the copy, and
// the hub is the one place every node already talks to with an identity
// it cannot fake.
//
// The receipt is the reverse guarantee. The hub signs what it recorded
// with the enrollment CA's key, which this node already pins, so a hub
// that later loses or removes a line is contradicted by a signature only
// it could have made, sitting in a file it cannot reach.
//
// # What this does not do
//
// It never holds up a job. A report is a request to a coalescing worker
// and returns at once; a hub that is slow, down, old or refusing costs a
// log line and nothing else. A node that stopped running jobs because it
// could not tell the hub about them would be a node an outage could
// switch off.
//
// And a receipt does not cause another report. Filing it moves the head,
// and reporting that would be answered with another receipt, which would
// move the head again: a loop between two machines with an fsync on each
// side of every turn. The receipt is reported with whatever is reported
// next -- the next job or the next connection -- which is soon enough,
// because what it protects is the record before it, and that record is
// already anchored.

// anchorState is the coalescing signal and the memory of what went
// wrong last.
type anchorState struct {
	// wanted holds at most one pending request. A burst of jobs ending
	// together is one report of the newest head, not one per job: the
	// newest head covers every record before it, so the intermediate
	// ones would add lines to the hub's file and protect nothing more.
	wanted chan struct{}
	// problem is the kind of the last failure, empty after a success.
	// A failure is logged when its kind differs from the last one and
	// is a debug line when it repeats, so a hub that answers 404 to every
	// report is said once rather than after every job for the life of
	// the agent.
	problem string
}

// requestAnchor asks for the head to be reported. It never blocks and is
// a no-op outside the agent, which is the only process that starts the
// reporter: a `halite-node call` writes no evidence worth a round trip.
func (n *node) requestAnchor() {
	if n.evidence == nil {
		return
	}
	n.evidence.mu.Lock()
	ch := n.evidence.anchor.wanted
	n.evidence.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
		// A report is already pending, and it will read the head when
		// it runs, which is after this record.
	}
}

// startAnchoring starts the agent's reporter, for as long as ctx lasts.
//
// The signal is installed before this returns and the worker started
// after, so that a request made the instant the stream opens is not lost
// to a goroutine that had not yet got round to listening.
//
// client is called for each report rather than captured once, because
// the agent rebuilds its client on every reconnect to pick up a renewed
// certificate, and a reporter holding the first one would present a
// serial the hub revoked at the renewal.
func (n *node) startAnchoring(ctx context.Context, client func() *transport.Client) {
	if n.evidence == nil || !n.evidenceOn() {
		return
	}
	ch := make(chan struct{}, 1)
	n.evidence.mu.Lock()
	n.evidence.anchor.wanted = ch
	n.evidence.mu.Unlock()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				n.reportHead(ctx, client())
			}
		}
	}()
}

// reportHead sends the current head and files the receipt.
func (n *node) reportHead(ctx context.Context, client *transport.Client) {
	log := n.evidenceLog()
	if log == nil {
		return
	}
	seq, hash := log.Head()
	if seq == 0 {
		return
	}
	res, err := client.AnchorEvidence(ctx, transport.EvidenceAnchorRequest{
		NodeID: n.nodeID, Seq: seq, Hash: hash,
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		n.anchorFailed(err, seq, hash)
		return
	}
	// The hub answers for the head it recorded, and that has to be the
	// head this node sent. A receipt for anything else is not a receipt
	// for this chain, however good its signature.
	if res.Seq != seq || res.Hash != hash {
		n.anchorProblem("mismatch", "the hub's receipt is not for the head this node reported; it is not filed",
			"seq", seq, "hash", hash, "receipt_seq", res.Seq, "receipt_hash", res.Hash)
		return
	}
	// Checked before it is filed. verify-evidence would find a bad one
	// later, but a node that files what it has not checked is writing
	// the hub's assertion into its own record as though it were a fact.
	if err := nodeevidence.VerifyReceipt(client.CA, res.NodeID, res.Seq, res.Hash, res.Received, res.Signature); err != nil {
		n.anchorProblem("receipt", "the hub's receipt does not verify against the CA this node pins; it is not filed",
			"seq", seq, "error", err.Error())
		return
	}
	// Through recordEvidence and not around it, so that the receipt is
	// counted and a failure to write it is said the way every other
	// record's is -- and deliberately without requestAnchor afterwards.
	n.recordEvidence(nodeevidence.KindAnchorReceipt, map[string]string{
		nodeevidence.ReceiptAnchoredSeq:  strconv.FormatUint(res.Seq, 10),
		nodeevidence.ReceiptAnchoredHash: res.Hash,
		nodeevidence.ReceiptNode:         res.NodeID,
		nodeevidence.ReceiptReceived:     res.Received,
		nodeevidence.ReceiptSignature:    res.Signature,
	})
	n.anchorRecovered(seq)
}

// anchorFailed sorts a failed report into the kinds an operator would
// act on differently.
func (n *node) anchorFailed(err error, seq uint64, hash string) {
	var refused *transport.RefusedError
	switch {
	case errors.As(err, &refused) && refused.Status == http.StatusNotFound:
		// A hub older than this node. Said once, at warn, because it
		// means this node's record is not anchored anywhere, and an
		// operator who believed it was should hear that.
		n.anchorProblem("unsupported",
			"the hub does not keep evidence anchors, so this node's evidence record is anchored nowhere; "+
				"upgrade the hub. Said once; later reports are debug lines",
			"error", err.Error())
	case transport.CodeOf(err) == transport.CodeEvidenceConflict:
		// The hub holds a head this chain contradicts. The hub has
		// already logged it, raised an event and counted it; this side
		// says so as an error once, so that somebody reading this
		// node's log is not the last to know.
		n.anchorProblem("conflict",
			"the hub's record of this node's evidence chain contradicts the chain; "+
				"it has been rewritten, reset or rolled back since it was last reported",
			"seq", seq, "hash", hash, "error", err.Error())
	default:
		n.anchorProblem("failed", "could not report this node's evidence head to the hub",
			"seq", seq, "error", err.Error())
	}
}

// anchorProblem logs a failure the first time its kind appears.
func (n *node) anchorProblem(kind, msg string, kv ...any) {
	n.evidence.mu.Lock()
	repeat := n.evidence.anchor.problem == kind
	n.evidence.anchor.problem = kind
	n.evidence.mu.Unlock()
	kv = append([]any{"component", "evidence"}, kv...)
	switch {
	case repeat:
		n.log.Debug(msg, kv...)
	case kind == "conflict":
		n.log.Error(msg, kv...)
	default:
		n.log.Warn(msg, kv...)
	}
}

// anchorRecovered clears the memory of a failure, saying so if there
// was one: a warning with no matching all-clear leaves a reader of the
// log believing the problem is still there.
func (n *node) anchorRecovered(seq uint64) {
	n.evidence.mu.Lock()
	was := n.evidence.anchor.problem
	n.evidence.anchor.problem = ""
	n.evidence.mu.Unlock()
	if was != "" {
		n.log.Info("the hub is recording this node's evidence head again",
			"component", "evidence", "seq", seq, "after", was)
	}
}
