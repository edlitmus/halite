// Package nodeevidence is SPEC 25.7's append-only, hash-chained record
// of what a node accepted and what changed underneath it.
//
// # What it is for, and what it cannot do
//
// It answers the compromised-hub row of SPEC 25.1's threat model. A hub
// that has been taken over can dispatch anything it likes and can then
// rewrite its own job cache, so the hub's account of what it sent is
// worth exactly as much as the hub is. This is the other account: kept
// on the node, written before the job runs, and chained so that an entry
// cannot be altered or removed without every entry after it failing to
// verify.
//
// The force of it is comparison. An investigator with a node's chain and
// a hub's job cache has two records that must agree, and the interesting
// case is the one where they do not.
//
// What it is not is tamper-proof. Anything running as root on the node
// can delete the file, or recompute the whole chain from the genesis
// record and produce a consistent forgery; a hash chain detects editing,
// not rewriting. Making the chain evidence against the node itself means
// anchoring its head hash somewhere the node cannot reach -- shipped to
// an operator, countersigned by a key the node does not hold -- and that
// is not built. Nothing here should be read as a claim about a
// compromised node.
//
// # The name
//
// "Evidence" already means something else in this tree:
// `internal/exec`'s Evidence is how much anybody has demonstrated about
// a *module*, answered by `sys.evidence` and gated by
// `make release-gate`. This package is unrelated to that, which is why
// it is not called `evidence`.
package nodeevidence

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Schema is the version of a record's shape.
//
// It is in the hash, and every field added after this one must carry
// `omitempty`, for the reason job.Job.Schema exists (DIVERGENCE 4.13): a
// record written by an older build is parsed by a newer one into a struct
// with more fields, and if an absent field encoded as a zero value the
// recomputed hash would not match and a rollback would report tampering.
// With `omitempty` the canonical bytes of an old record are the same
// under both builds.
//
// A chain whose records carry a schema this build does not know is
// readable and is not verifiable here, and Verify says which of those
// two it is rather than calling it a break.
const Schema = "halite.evidence/1"

// The record kinds. Stable tokens: they are in the hash and in files an
// investigator reads years later.
const (
	// KindJobAccepted is written before a job runs, which is the only
	// useful moment. A record written afterwards is missing exactly the
	// jobs that stopped the node.
	KindJobAccepted = "job.accepted"
	// KindJobRefused is a job the node would not run, and why. SPEC 6.3
	// already requires the refusal to travel back to the operator; this
	// is the half that survives a hub that drops it.
	KindJobRefused = "job.refused"
	// KindJobResult pairs with KindJobAccepted by jid.
	KindJobResult = "job.result"
	// KindConfig is the configuration in effect, recorded at startup.
	// See Digest: the record holds a digest and the file list, never a
	// value.
	KindConfig = "config"
	// KindExtension is one extension bundle appearing, changing version
	// or being refused, as `saltutil.sync_*` reports it.
	KindExtension = "extension"
	// KindStart and KindStop bracket an agent's run, so that a gap in
	// the record can be told from a node that was switched off.
	KindStart = "node.start"
	KindStop  = "node.stop"
)

// Record is one line of the chain.
type Record struct {
	Schema string `json:"schema"`
	Seq    uint64 `json:"seq"`
	// TS is RFC 3339 with microseconds, as SPEC 26.1 spells timestamps.
	//
	// The node's clock, and worth nothing more than the node's clock is
	// worth: a host whose time is wrong writes a chain whose order is
	// still sound, because the order is Seq and the links, and whose
	// timestamps are not. Verify checks the links and says nothing about
	// the clock.
	TS     string `json:"ts"`
	Kind   string `json:"kind"`
	NodeID string `json:"node_id,omitempty"`
	// Lost counts the records that could not be appended between the
	// previous record and this one.
	//
	// A chain that simply skipped them would be a chain with a silent
	// hole in it, which is the one thing an audit record must not have.
	// It cannot be perfectly accurate -- a write that fails because the
	// filesystem is full may also lose this counter when the process
	// dies -- and it is the difference between "something is missing
	// here" and nothing at all.
	Lost int `json:"lost,omitempty"`
	// Detail is the payload, as sorted string pairs.
	//
	// Strings, and a flat map, because the hash has to be reproducible
	// by anything that reads the file. `encoding/json` sorts map keys,
	// so this encodes identically everywhere; a nested `any` would
	// encode by whatever the parser happened to produce, and a number
	// through json.Number and back is not guaranteed to be the same
	// text.
	Detail map[string]string `json:"detail,omitempty"`
	// Prev is the previous record's Hash, empty in the genesis record.
	Prev string `json:"prev"`
	// Hash covers every field above, including Prev.
	Hash string `json:"hash"`
}

// hashOf computes a record's hash over its canonical encoding.
//
// The canonical encoding is the record with Hash emptied, marshalled by
// `encoding/json`: struct fields in declaration order, map keys sorted.
func hashOf(r Record) (string, error) {
	r.Hash = ""
	canonical, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("encoding an evidence record: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Options configure a log.
type Options struct {
	// NodeID is stamped on every record.
	NodeID string
	// MaxBytes seals the current segment once it is this large. Zero
	// uses DefaultMaxBytes.
	MaxBytes int64
	// Now is the clock, for the tests.
	Now func() time.Time
}

// DefaultMaxBytes seals a segment at 64 MiB, which is a few hundred
// thousand records.
//
// Segments exist because one file that grows for the life of a host is a
// file nobody can read and, eventually, a full disk. Sealing is not
// pruning: nothing in this package deletes a segment, because a record
// the audited system can delete is not evidence. An estate that keeps
// nodes for years ships the sealed segments off and removes them
// deliberately, and `verify-evidence` reports what it has.
const DefaultMaxBytes = 64 << 20

// currentName is the segment being appended to. A sealed segment is
// named for the sequence number it starts at, zero-padded so that
// lexical order is chain order.
const currentName = "evidence.jsonl"

func sealedName(firstSeq uint64) string {
	return fmt.Sprintf("evidence.%012d.jsonl", firstSeq)
}

// Log is the append-only chain. Safe for concurrent use.
type Log struct {
	dir      string
	nodeID   string
	maxBytes int64
	now      func() time.Time

	mu sync.Mutex
	// file is the current segment, held open for the process's life:
	// reopening per record would be three syscalls to save nothing, and
	// the descriptor is what makes an append one write.
	file *os.File
	size int64
	// firstSeq is the sequence number the current segment starts at,
	// which is the name it is sealed under.
	firstSeq uint64
	seq      uint64
	head     string
	lost     int
}

// Open prepares the chain in dir, creating it if necessary, and recovers
// the head of an existing one.
//
// A directory that exists is not a directory this process can use: a
// chain left behind by a hand-run as root is opened without complaint by
// an agent running as its service account, which can write nothing in it,
// and the symptom would be an audit record that quietly stops. Probed
// here so it is one message at startup naming the directory, which is
// what OpenNodeCache learned to do for the same reason.
func Open(dir string, opts Options) (*Log, error) {
	if dir == "" {
		return nil, errors.New("the evidence log needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the evidence directory: %w", err)
	}
	probe := filepath.Join(dir, ".writable")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return nil, fmt.Errorf("the evidence directory at %s is not usable by this process: %w", dir, err)
	}
	if err := os.Remove(probe); err != nil {
		return nil, fmt.Errorf("the evidence directory at %s is not usable by this process: %w", dir, err)
	}

	l := &Log{
		dir:      dir,
		nodeID:   opts.NodeID,
		maxBytes: opts.MaxBytes,
		now:      opts.Now,
	}
	if l.maxBytes <= 0 {
		l.maxBytes = DefaultMaxBytes
	}
	if l.now == nil {
		l.now = time.Now
	}
	if err := l.recover(); err != nil {
		return nil, err
	}
	return l, nil
}

// recover reads the tail of the chain so that the next record links to
// it.
//
// Two states are recovered rather than refused. A current segment that
// is absent while sealed ones are present is a process killed between
// the rename and the create, so the chain continues from the newest
// sealed segment. And a current segment whose last line is incomplete is
// a process killed mid-append: the partial bytes are dropped and the
// next record says how many were lost, because the alternative is either
// refusing to record anything ever again or appending after a line that
// no reader can parse.
func (l *Log) recover() error {
	segments, err := Segments(l.dir)
	if err != nil {
		return err
	}
	var tail *Record
	for i := len(segments) - 1; i >= 0 && tail == nil; i-- {
		last, partial, err := lastRecord(segments[i])
		if err != nil {
			return err
		}
		if partial > 0 {
			l.lost++
		}
		tail = last
	}

	current := filepath.Join(l.dir, currentName)
	if tail != nil {
		l.seq, l.head = tail.Seq, tail.Hash
		if tail.Schema != Schema {
			// Appending would extend a chain this build cannot
			// canonicalise, so every record after it would read as
			// broken to the build that wrote the earlier ones. Refused
			// with the schema named, which is the readable-but-not-
			// writable rule job.Job.Schema settled on.
			return fmt.Errorf(
				"the evidence chain in %s was written by a build using schema %q and this build writes %q; "+
					"`halite-node verify-evidence` still reads it",
				l.dir, tail.Schema, Schema)
		}
	}
	// Truncate a partial tail before opening for append. Done to the
	// current segment only: a sealed one is not written again, and a
	// partial line in one is a break Verify reports rather than
	// something to repair behind an investigator's back.
	if err := truncatePartial(current); err != nil {
		return err
	}
	f, err := os.OpenFile(current, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening the evidence log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("reading the evidence log: %w", err)
	}
	l.file, l.size = f, info.Size()
	// The first sequence number in this segment, which is the name it
	// will be sealed under. A segment that is empty starts at the next
	// record; one that is not is named for the record it already holds.
	l.firstSeq = l.seq + 1
	if info.Size() > 0 {
		first, err := firstRecord(current)
		if err != nil {
			f.Close()
			return err
		}
		if first != nil {
			l.firstSeq = first.Seq
		}
	}
	return nil
}

// Append writes one record and waits for it to reach the disk.
//
// Synchronous, because the caller's next act is to run the job. A record
// still in the page cache when the machine is powered off is a record of
// the job that was running when somebody pulled the plug, which is the
// one worth having.
func (l *Log) Append(kind string, detail map[string]string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	r := Record{
		Schema: Schema,
		Seq:    l.seq + 1,
		TS:     l.now().UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
		Kind:   kind,
		NodeID: l.nodeID,
		Lost:   l.lost,
		Detail: detail,
		Prev:   l.head,
	}
	hash, err := hashOf(r)
	if err != nil {
		return err
	}
	r.Hash = hash
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encoding an evidence record: %w", err)
	}
	line = append(line, '\n')

	if l.size > 0 && l.size+int64(len(line)) > l.maxBytes {
		if err := l.seal(); err != nil {
			return err
		}
	}
	// One Write, so that a crash leaves at most one partial line, which
	// recover drops and the next record counts.
	if _, err := l.file.Write(line); err != nil {
		l.lost++
		return fmt.Errorf("appending to the evidence log: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		l.lost++
		return fmt.Errorf("flushing the evidence log: %w", err)
	}
	l.size += int64(len(line))
	l.seq, l.head, l.lost = r.Seq, r.Hash, 0
	return nil
}

// seal closes the current segment and starts a new one. Called with the
// lock held.
//
// Nothing is written to mark the boundary: the first record of the new
// segment carries the last record's hash as its Prev, exactly as it
// would inside one file, so the chain crosses a segment boundary without
// a record that means "a file ended here" and could be forged to hide
// one.
func (l *Log) seal() error {
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("closing the evidence segment: %w", err)
	}
	current := filepath.Join(l.dir, currentName)
	target := filepath.Join(l.dir, sealedName(l.firstSeq))
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("sealing the evidence segment: %s already exists", target)
	}
	if err := os.Rename(current, target); err != nil {
		return fmt.Errorf("sealing the evidence segment: %w", err)
	}
	f, err := os.OpenFile(current, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening the evidence log: %w", err)
	}
	l.file, l.size, l.firstSeq = f, 0, l.seq+1
	return nil
}

// Head is the last record's hash and sequence number, which is what an
// operator copies somewhere the node cannot reach.
func (l *Log) Head() (uint64, string) {
	if l == nil {
		return 0, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq, l.head
}

// Close releases the segment.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Segments lists the chain's files in chain order: sealed segments by
// their zero-padded first sequence number, then the current one.
func Segments(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the evidence directory: %w", err)
	}
	var sealed []string
	current := ""
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case name == currentName:
			current = filepath.Join(dir, name)
		case strings.HasPrefix(name, "evidence.") && strings.HasSuffix(name, ".jsonl"):
			sealed = append(sealed, filepath.Join(dir, name))
		}
	}
	sort.Strings(sealed)
	if current != "" {
		sealed = append(sealed, current)
	}
	return sealed, nil
}

// scanLines reads a segment line by line, calling fn with each complete
// line. It returns the number of bytes in a trailing incomplete line.
//
// A long line is refused rather than silently split: bufio.Scanner would
// stop at its own limit and a reader that stops early would call a
// complete chain truncated.
func scanLines(path string, fn func(lineNo int, line []byte) error) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	lineNo := 0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			lineNo++
			if err := fn(lineNo, line[:len(line)-1]); err != nil {
				return 0, err
			}
			continue
		}
		// No newline: either the end of the file or a partial record.
		if err == io.EOF {
			return len(line), nil
		}
		if err != nil {
			return 0, fmt.Errorf("reading %s: %w", path, err)
		}
	}
}

// firstRecord reads a segment's first parseable record.
func firstRecord(path string) (*Record, error) {
	var found *Record
	sentinel := errors.New("stop")
	_, err := scanLines(path, func(_ int, line []byte) error {
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil
		}
		found = &r
		return sentinel
	})
	if err != nil && !errors.Is(err, sentinel) {
		return nil, err
	}
	return found, nil
}

// lastRecord reads a segment's last parseable record and the size of any
// trailing partial line.
func lastRecord(path string) (*Record, int, error) {
	var found *Record
	partial, err := scanLines(path, func(_ int, line []byte) error {
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			// Left to Verify to report. recover's job is to find
			// something to link to, and an unparseable line is not it.
			return nil
		}
		found = &r
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return found, partial, nil
}

// truncatePartial removes a trailing incomplete line from the current
// segment, so that the next record is a whole line on a line of its own.
func truncatePartial(path string) error {
	partial, err := scanLines(path, func(int, []byte) error { return nil })
	if err != nil {
		return err
	}
	if partial == 0 {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if err := os.Truncate(path, info.Size()-int64(partial)); err != nil {
		return fmt.Errorf("repairing the tail of %s: %w", path, err)
	}
	return nil
}
