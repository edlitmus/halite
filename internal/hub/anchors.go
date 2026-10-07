package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/nodeevidence"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/ratelimit"
	"github.com/edlitmus/halite/internal/transport"
)

// AnchorStore is the hub's record of the evidence heads its nodes have
// reported: SPEC 25.7's anchor, one append-only file per node.
//
// # What it is for
//
// A node's evidence chain detects editing and not rewriting: anything
// with root on the node can recompute the whole chain from its first
// record and produce a forgery that verifies. The only defence against
// that is a copy of the head somewhere the node cannot write, and this is
// that copy. Once the hub holds (seq, hash) for a node, a chain that no
// longer has that hash at that number has been rewritten since, and
// `halite-node verify-evidence --anchors` says so.
//
// The receipt is the same idea pointed the other way. The hub signs what
// it recorded with the enrollment CA's key and the node files the
// signature in its own chain, so a hub that later drops a line from this
// file is contradicted by a signature only it could have made.
//
// # What it is not
//
// It protects what had been reported. Records a node wrote after its
// last report are as rewritable as they ever were, and a node and hub
// compromised together can agree on anything.
//
// Nor is its real-time conflict check the guarantee. A rewritten chain
// that is padded past the highest number the hub holds before it is next
// reported is accepted here, because a head alone carries nothing that
// links it to the heads before it. What catches that chain is the
// comparison verify-evidence makes against every accepted line, which
// the padding does not change.
//
// # The format
//
// One nodeevidence.Anchor per line, JSON, appended and never rewritten.
// JSON lines rather than a record per file or a database because the
// file is meant to be handed to somebody: `halite-hub evidence anchors`
// prints it as stored, and an investigator copies it next to a node's
// chain without anything in between that could reinterpret it.
type AnchorStore struct {
	dir string

	// Rate and Burst bound how often one node may report, as a token
	// bucket: Rate reports a second sustained, Burst at once. Zero takes
	// the defaults. See Allow.
	Rate  float64
	Burst int

	// Warn, when set, is told when a node's file is repaired on load:
	// an incomplete last line dropped, or a complete one given the
	// newline it was missing. Optional so that the store can be used
	// without a logger, as `halite-hub evidence anchors` and the tests
	// do; serve sets it.
	Warn func(msg string, kv ...any)

	mu    sync.Mutex
	nodes map[string]*nodeAnchors
}

// nodeAnchors is what the store remembers about one node between
// requests: the highest head it accepted, read from the file once.
//
// One lock per node rather than one for the store, because every write
// here is fsynced and a hub restarting under a thousand nodes hears a
// thousand reports in the same second. Serialising those on one lock
// would make the last node wait for nine hundred and ninety-nine disk
// writes that have nothing to do with it.
type nodeAnchors struct {
	mu     sync.Mutex
	loaded bool
	top    *nodeevidence.Anchor

	// The node's token bucket, under its own lock so that a refused
	// report never waits behind another report's fsync.
	rateMu sync.Mutex
	bucket ratelimit.Bucket
}

// DefaultAnchorRate and DefaultAnchorBurst are the bucket a node gets
// unless `evidence_anchor_rate` and `evidence_anchor_burst` say
// otherwise.
//
// A node reports when its stream opens and after each job, one report
// in flight at a time, so its honest rate is its job rate. A burst of 60
// covers a highstate's worth of jobs finishing together, or a
// reconnecting node; one a second sustained is more than an estate runs
// jobs on one machine. What it bounds is a compromised node, which could
// otherwise report an ever-larger number as fast as the hub would fsync
// it: about 86,000 lines a day at the default, where before there was no
// limit at all. DIVERGENCE 5.231.
const (
	DefaultAnchorRate  = 1.0
	DefaultAnchorBurst = 60
)

// Allow takes one report from the node's bucket, and says whether there
// was one to take.
//
// It is checked before anything else the report costs -- reading the
// node's file, signing, the fsync -- because those are what a flood is
// trying to spend. A refused report is not written anywhere: the node's
// next report carries a later head, which covers this one, so a busy
// honest node is anchored a little later rather than not at all.
func (a *AnchorStore) Allow(nodeID string, now time.Time) bool {
	rate, burst := a.Rate, a.Burst
	if rate <= 0 {
		rate = DefaultAnchorRate
	}
	if burst < 1 {
		burst = DefaultAnchorBurst
	}
	n := a.node(nodeID)
	n.rateMu.Lock()
	defer n.rateMu.Unlock()
	return n.bucket.Take(now, rate, burst)
}

// OpenAnchorStore prepares the store's directory.
func OpenAnchorStore(dir string) (*AnchorStore, error) {
	if dir == "" {
		return nil, errors.New("the evidence anchors need a directory")
	}
	if err := fileperm.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the evidence anchor directory: %w", err)
	}
	return &AnchorStore{dir: dir, nodes: map[string]*nodeAnchors{}}, nil
}

// AnchorPath is where one node's anchors are kept, refusing a name that
// is not a node identity so that a name cannot become a path.
//
// Exported for `halite-hub evidence anchors`, which reads the file
// without a running hub and must find the one this store writes.
func AnchorPath(dir, nodeID string) (string, error) {
	if err := pki.ValidateNodeID(nodeID); err != nil {
		return "", err
	}
	return filepath.Join(dir, nodeID+".jsonl"), nil
}

func (a *AnchorStore) node(nodeID string) *nodeAnchors {
	a.mu.Lock()
	defer a.mu.Unlock()
	n, ok := a.nodes[nodeID]
	if !ok {
		n = &nodeAnchors{}
		a.nodes[nodeID] = n
	}
	return n
}

// read parses one node's file. An absent file is a node that has never
// reported.
func readAnchorFile(path string) ([]nodeevidence.Anchor, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	anchors, err := nodeevidence.ReadAnchors(f)
	if err != nil {
		// Refused rather than worked around. A line this hub cannot
		// read is either damage or an edit, and deciding which heads
		// count while ignoring it would be deciding on less than the
		// record holds. The node's reports fail with this message until
		// somebody looks, which is the point.
		return nil, fmt.Errorf("the evidence anchors at %s: %w", path, err)
	}
	return anchors, nil
}

// Record files one reported head and says what the hub made of it.
//
// sign is called for a head being accepted, with the timestamp being
// recorded, and its signature is stored on the line and returned as the
// receipt. It is called before the line is written and the line is
// written before anything is returned, so there is never a receipt for a
// head the file does not hold.
//
// The same head reported again is answered from the line already
// written, receipt and all, and adds nothing: a node that reconnects
// without having written a record re-reports what it reported last time,
// and a line per reconnect would bury the lines that mean something.
func (a *AnchorStore) Record(nodeID string, seq uint64, hash string, now time.Time,
	sign func(received string) (string, error)) (*nodeevidence.Anchor, error) {
	path, err := AnchorPath(a.dir, nodeID)
	if err != nil {
		return nil, err
	}
	n := a.node(nodeID)
	n.mu.Lock()
	defer n.mu.Unlock()

	// A file that has gone since it was read is an operator who moved
	// it aside -- docs/operations.md says to, once a node's chain has
	// legitimately started again -- and the memory of it goes with it.
	// Without this a running hub would go on comparing the node against
	// a record nobody can see any more, and write conflicts into the new
	// file against hashes that are not in it.
	if n.loaded {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			n.loaded, n.top = false, nil
		}
	}
	if !n.loaded {
		if err := a.repairTail(nodeID, path); err != nil {
			return nil, err
		}
		anchors, err := readAnchorFile(path)
		if err != nil {
			return nil, err
		}
		for i := range anchors {
			if anchors[i].Result == nodeevidence.AnchorAccepted &&
				(n.top == nil || anchors[i].Seq > n.top.Seq) {
				top := anchors[i]
				n.top = &top
			}
		}
		n.loaded = true
	}

	line := nodeevidence.Anchor{Seq: seq, Hash: hash, Received: nodeevidence.ReceivedNow(now)}
	switch {
	case n.top != nil && seq == n.top.Seq && hash == n.top.Hash:
		repeat := *n.top
		return &repeat, nil

	case n.top != nil && seq <= n.top.Seq:
		line.Result = nodeevidence.AnchorConflict
		line.PriorSeq, line.PriorHash = n.top.Seq, n.top.Hash
		if seq < n.top.Seq {
			// The hash the hub accepted at this number, if it accepted
			// one, is the contradiction worth naming: "record 40 was X
			// and is now Y" says more than "you were at 90". The file is
			// read again for it because this is the rare path, and
			// keeping every head of every node in memory to make it
			// cheap would make the common one expensive.
			anchors, err := readAnchorFile(path)
			if err != nil {
				return nil, err
			}
			for _, prior := range anchors {
				if prior.Result == nodeevidence.AnchorAccepted && prior.Seq == seq {
					line.PriorSeq, line.PriorHash = prior.Seq, prior.Hash
					break
				}
			}
		}

	default:
		line.Result = nodeevidence.AnchorAccepted
		receipt, err := sign(line.Received)
		if err != nil {
			return nil, err
		}
		line.Receipt = receipt
	}

	if err := appendAnchor(path, line); err != nil {
		return nil, err
	}
	if line.Result == nodeevidence.AnchorAccepted {
		accepted := line
		n.top = &accepted
	}
	return &line, nil
}

// repairTail makes a node's file end on a whole line before it is read,
// which is the state a hub that stopped mid-append leaves it in.
//
// appendAnchor is one write and nothing is answered until it has been
// fsynced, so a last line with no newline is a line no receipt was ever
// sent for. Two cases, and neither loses anything acknowledged:
//
//   - The tail does not parse. It is the front of a line whose write
//     never finished, and it is truncated away -- what
//     nodeevidence.Log.recover does to the current segment of a chain,
//     for the same reason. The node's next report carries a head at
//     least as high and is answered then.
//   - The tail parses as a whole anchor and only the newline is
//     missing. It is kept and terminated: the head on it is one the
//     node really did report, and a complete record is not this
//     function's to throw away.
//
// Before this, readAnchorFile refused the file -- correctly for a line
// in the middle, which is damage or an edit -- and so refused every
// report the node made until somebody edited the file by hand
// (DIVERGENCE 5.229's "not covered"). A broken line that does end in a
// newline is still refused: only the one shape a crash produces is
// repaired, and only at the end.
func (a *AnchorStore) repairTail(nodeID, path string) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || len(raw) == 0 || raw[len(raw)-1] == '\n' {
		return nil
	}
	if err != nil {
		return fmt.Errorf("the evidence anchors at %s: %w", path, err)
	}
	start := bytes.LastIndexByte(raw, '\n') + 1
	tail := raw[start:]
	if whole, err := nodeevidence.ReadAnchors(bytes.NewReader(tail)); err == nil && len(whole) == 1 {
		f, err := fileperm.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("repairing the evidence anchors at %s: %w", path, err)
		}
		_, werr := f.Write([]byte{'\n'})
		serr := f.Sync()
		if cerr := f.Close(); werr == nil && serr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = serr
		}
		if werr != nil {
			return fmt.Errorf("repairing the evidence anchors at %s: %w", path, werr)
		}
		a.warn("the last evidence anchor line for a node was missing its newline, "+
			"from a hub that stopped while writing it; the line was whole and is kept",
			"node_id", nodeID, "path", path, "seq", whole[0].Seq)
		return nil
	}
	if err := truncateSync(path, int64(start)); err != nil {
		return fmt.Errorf("repairing the evidence anchors at %s: %w", path, err)
	}
	a.warn("dropped an incomplete last line from a node's evidence anchors, "+
		"left by a hub that stopped while writing it; no receipt was issued for it",
		"node_id", nodeID, "path", path, "bytes", len(tail))
	return nil
}

func (a *AnchorStore) warn(msg string, kv ...any) {
	if a.Warn != nil {
		a.Warn(msg, kv...)
	}
}

// truncateSync cuts a file to size and waits for that to reach the disk,
// so that a repair is not undone by the same power loss that made it
// necessary.
func truncateSync(path string, size int64) error {
	f, err := fileperm.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// appendAnchor writes one line and waits for it to reach the disk.
//
// Synchronous for the reason nodeevidence.Log.Append is: the receipt
// goes back to the node the moment this returns, and a receipt for a
// line still in the page cache when the hub loses power is a receipt the
// hub's own file then contradicts.
//
// A write or flush that fails cuts the file back to where it was. The
// hub is still running, so repairTail -- which runs only when a node's
// file is first read -- would not see the partial line, and the next
// report would be appended after it: a broken line in the middle of the
// file, which every reader refuses and nothing repairs. Nothing is
// answered for a line that failed, so taking it back loses nothing
// acknowledged.
func appendAnchor(path string, line nodeevidence.Anchor) error {
	raw, err := json.Marshal(line)
	if err != nil {
		return err
	}
	f, err := fileperm.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening the evidence anchors: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("reading the evidence anchors: %w", err)
	}
	// The cut goes through a second handle, opened after this one is
	// closed. This handle is O_APPEND, and on Windows that is an open
	// for appending only: Truncate on it fails with "Access is denied",
	// which left the fragment in place on the Windows legs.
	undo := func(cause error) error {
		f.Close()
		if terr := truncateSync(path, info.Size()); terr != nil {
			cause = fmt.Errorf("%w; and cutting the partial line back failed too: %v", cause, terr)
		}
		return cause
	}
	// One write, so that a crash leaves at most one partial line.
	if _, err := appendWrite(f, append(raw, '\n')); err != nil {
		return undo(fmt.Errorf("appending to the evidence anchors: %w", err))
	}
	if err := f.Sync(); err != nil {
		return undo(fmt.Errorf("flushing the evidence anchors: %w", err))
	}
	return f.Close()
}

// appendWrite is the one write appendAnchor makes, a variable so that a
// test can make it fail part-way, which a real disk does only when it is
// full.
var appendWrite = func(f *os.File, b []byte) (int, error) { return f.Write(b) }

var errNoAnchors = errors.New("this hub keeps no evidence anchors")

// evidenceAnchor is POST /v1/evidence/anchor.
//
// The node identity is the certificate's, from authenticated, and the
// body cannot change it: a node that could name the node it was
// anchoring for could fill another node's record with heads that
// contradict its real chain.
func (s *Server) evidenceAnchor(w http.ResponseWriter, r *http.Request, nodeID string) {
	var req transport.EvidenceAnchorRequest
	if err := transport.ReadJSON(w, r, 64<<10, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if req.NodeID != "" && req.NodeID != nodeID {
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("the certificate says %s and the request says %s", nodeID, req.NodeID))
		return
	}
	if err := nodeevidence.CheckHead(req.Seq, req.Hash); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if s.Anchors == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal, errNoAnchors)
		return
	}
	if s.Authority == nil || s.Authority.CA == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub holds no enrollment CA to sign a receipt with"))
		return
	}
	if !s.Anchors.Allow(nodeID, s.now()) {
		// Counted rather than logged: a node over its rate is reporting
		// many times a second, and a line for each would be the flood
		// moved into the hub's log.
		s.m().evidenceAnchors.With("rate_limited").Inc()
		transport.WriteError(w, http.StatusTooManyRequests, transport.CodeRateLimited,
			errors.New("this node is reporting its evidence head faster than the hub records it; "+
				"its next report will carry a later head"))
		return
	}

	line, err := s.Anchors.Record(nodeID, req.Seq, req.Hash, s.now(), func(received string) (string, error) {
		return nodeevidence.SignReceipt(s.Authority.CA.Key, nodeID, req.Seq, req.Hash, received)
	})
	if err != nil {
		s.m().evidenceAnchors.With("failed").Inc()
		s.warn("could not record a node's evidence head", "node_id", nodeID,
			"seq", req.Seq, "error", err.Error())
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal,
			errors.New("the hub could not record this head; its log says why"))
		return
	}

	if line.Result == nodeevidence.AnchorConflict {
		s.m().evidenceAnchors.With(nodeevidence.AnchorConflict).Inc()
		s.warn("a node's evidence chain contradicts the head it reported before; "+
			"it has been rewritten, reset or rolled back",
			"node_id", nodeID, "seq", req.Seq, "hash", req.Hash,
			"prior_seq", line.PriorSeq, "prior_hash", line.PriorHash)
		s.emit(tagEvidenceConflict(nodeID), nodeID, map[string]any{
			"seq":        strconv.FormatUint(req.Seq, 10),
			"hash":       req.Hash,
			"prior_seq":  strconv.FormatUint(line.PriorSeq, 10),
			"prior_hash": line.PriorHash,
		})
		transport.WriteError(w, http.StatusConflict, transport.CodeEvidenceConflict,
			fmt.Errorf("record %d as %s contradicts record %d as %s, which this hub recorded earlier; "+
				"no receipt is issued", req.Seq, req.Hash, line.PriorSeq, line.PriorHash))
		return
	}

	s.m().evidenceAnchors.With(nodeevidence.AnchorAccepted).Inc()
	transport.WriteJSON(w, http.StatusOK, transport.EvidenceAnchorResponse{
		NodeID:    nodeID,
		Seq:       line.Seq,
		Hash:      line.Hash,
		Received:  line.Received,
		Signature: line.Receipt,
	})
}
