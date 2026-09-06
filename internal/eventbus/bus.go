package eventbus

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bus is a segmented append-only log.
type Bus struct {
	dir string

	mu      sync.Mutex
	current *os.File
	segment int
	written int64
	// closed records that Close has run, so that a later Append is
	// refused rather than silently reopening the segment.
	closed bool

	// waiters are woken when a record is appended, so a follower does
	// not poll a file that is not changing.
	waitMu  sync.Mutex
	waiters []chan struct{}

	// SegmentBytes caps one file. A segment is the unit of retention,
	// so a very large one means retention that binds late.
	SegmentBytes int64
	// Retention and MaxBytes are the two limits of SPEC 17.2,
	// whichever binds first.
	Retention time.Duration
	MaxBytes  int64
	// FsyncAlways are the tag globs written durably before the append
	// returns. SPEC 17.2 names the security-relevant classes; the rest
	// are flushed and synced on an interval.
	FsyncAlways []string
	Now         func() time.Time
}

// Defaults for the limits above.
const (
	DefaultSegmentBytes = 32 << 20
	DefaultRetention    = 30 * 24 * time.Hour
	DefaultMaxBytes     = 4 << 30
)

// SecurityTags are the classes SPEC 17.2 writes durably: an
// authentication attempt or a key action that is lost in a crash is
// exactly the record an audit needs.
var SecurityTags = []string{"halite/auth**", "halite/key/**"}

// Open prepares a bus.
func Open(dir string) (*Bus, error) {
	if dir == "" {
		return nil, fmt.Errorf("the event bus needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the event bus: %w", err)
	}
	b := &Bus{
		dir:          dir,
		SegmentBytes: DefaultSegmentBytes,
		Retention:    DefaultRetention,
		MaxBytes:     DefaultMaxBytes,
		FsyncAlways:  SecurityTags,
	}
	segments, err := b.segments()
	if err != nil {
		return nil, err
	}
	if len(segments) > 0 {
		b.segment = segments[len(segments)-1]
	} else {
		b.segment = 1
	}
	return b, nil
}

func (b *Bus) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Dir is where the log lives.
func (b *Bus) Dir() string { return b.dir }

func (b *Bus) path(seq int) string {
	return filepath.Join(b.dir, fmt.Sprintf("%08d.ndjson", seq))
}

// segments lists the sequence numbers present, in order.
func (b *Bus) segments() ([]int, error) {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return nil, fmt.Errorf("reading the event bus at %s: %w", b.dir, err)
	}
	var out []int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		seq, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".ndjson"))
		if err != nil {
			continue
		}
		out = append(out, seq)
	}
	sort.Ints(out)
	return out, nil
}

// Append writes one event and returns its offset.
func (b *Bus) Append(e *Event) (string, error) {
	if err := ValidTag(e.Tag); err != nil {
		return "", err
	}
	if e.Stamp.IsZero() {
		e.Stamp = b.now()
	}
	line, err := e.encode()
	if err != nil {
		return "", err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.openCurrent(); err != nil {
		return "", err
	}
	if b.SegmentBytes > 0 && b.written+int64(len(line)) > b.SegmentBytes && b.written > 0 {
		if err := b.rotate(); err != nil {
			return "", err
		}
	}
	if _, err := b.current.Write(line); err != nil {
		return "", fmt.Errorf("appending to the event bus: %w", err)
	}
	b.written += int64(len(line))
	offset := fmt.Sprintf("%08d:%d", b.segment, b.written)

	if MatchAny(b.FsyncAlways, e.Tag) {
		if err := b.current.Sync(); err != nil {
			return "", fmt.Errorf("syncing the event bus: %w", err)
		}
	}
	b.wake()
	return offset, nil
}

// openCurrent opens the segment being written, unless the bus is shut.
//
// The closed check is the point. Close set current to nil and this
// reopened the file on the next append, so a bus that had been closed
// went on accepting events and holding the segment open for the life of
// the process. Shutdown ordering was therefore unenforceable — a caller
// could not tell whether an event after Close had been recorded — and
// on Windows the leaked handle stopped the directory being removed,
// which is how it was found.
func (b *Bus) openCurrent() error {
	if b.closed {
		return fmt.Errorf("the event bus is closed")
	}
	if b.current != nil {
		return nil
	}
	file, err := os.OpenFile(b.path(b.segment), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening the event bus segment: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	b.current = file
	b.written = info.Size()
	return nil
}

func (b *Bus) rotate() error {
	if err := b.current.Sync(); err != nil {
		return err
	}
	if err := b.current.Close(); err != nil {
		return err
	}
	b.current = nil
	b.segment++
	b.written = 0
	return b.openCurrent()
}

// Sync flushes what has not been written durably yet, for the
// `fsync: interval` class of SPEC 17.2.
func (b *Bus) Sync() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.current == nil {
		return nil
	}
	return b.current.Sync()
}

// Close releases the current segment.
func (b *Bus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	if b.current == nil {
		return nil
	}
	err := b.current.Sync()
	closeErr := b.current.Close()
	b.current = nil
	if err != nil {
		return err
	}
	return closeErr
}

// The positions a subscriber may start from, per SPEC 17.2.
const (
	Earliest = "earliest"
	Latest   = "latest"
)

// ErrBadOffset is returned for an offset that is not one this bus
// issued.
var ErrBadOffset = errors.New("that is not an offset on this bus")

// ErrSubscriberLag is SPEC 17.2's named error: a subscriber asking to
// resume from a position the bus has since pruned.
//
// It is deliberately not ErrBadOffset. The offset was issued by this bus
// and was valid when it was handed out; what has happened is that the
// subscriber fell further behind than the retention window is wide. The
// two call for different answers — a malformed offset says nothing about
// where the reader was, so there is nowhere to resume but the end, while
// a lagged one says exactly where it was, and the oldest surviving event
// is the nearest point to it.
//
// Returning it at all is the whole argument of SPEC 17.2. This build
// used to skip forward to the oldest surviving segment and return events
// from there with no indication that anything was missed; the chaos
// scenario measured 380 events silently skipped. That is what Salt's bus
// does — checked against 3007.1 and 3008.2, which have no offset,
// replay or resume at any level and drop at a ZeroMQ high-water mark of
// 1000 without telling anyone — and SPEC 17.2 names it as the thing a
// durable log exists not to do. DIVERGENCE 4.12.
var ErrSubscriberLag = errors.New("subscriber_lag")

// LagError says how far behind a subscriber fell, because "your offset
// is gone" without a number cannot be acted on.
//
// Oldest is what to resume from to lose the least. A caller that wants
// to carry on rather than stop uses it and knows what it skipped.
type LagError struct {
	// From is the offset that was asked for.
	From string
	// Oldest is the oldest offset the bus still holds.
	Oldest string
	// Segments is how many whole segments were pruned from under the
	// subscriber. It is a lower bound on what was missed rather than an
	// event count: the bus prunes by segment and does not keep a tally
	// of what was in one it deleted.
	Segments int
}

func (e *LagError) Error() string {
	return fmt.Sprintf(
		"subscriber_lag: the offset %s has been pruned; %d whole segment(s) were "+
			"removed from under it and the oldest event the bus still holds is at %s",
		e.From, e.Segments, e.Oldest)
}

func (e *LagError) Is(target error) bool { return target == ErrSubscriberLag }

// Lag reports whether a subscriber resuming from this offset has fallen
// off the back of the bus, without reading anything.
//
// Separate from Read so that a caller streaming to a client can refuse
// before it writes a success header. `latest` and `earliest` cannot lag:
// they are resolved against what exists now.
func (b *Bus) Lag(from string) error {
	switch from {
	case "", Latest, Earliest:
		return nil
	}
	segment, _, err := parseOffset(from)
	if err != nil {
		return nil // malformed is ErrBadOffset's business, not this one
	}
	segments, err := b.segments()
	if err != nil {
		return err
	}
	if len(segments) == 0 || segment >= segments[0] {
		return nil
	}
	pruned := 0
	for seq := segment; seq < segments[0]; seq++ {
		pruned++
	}
	return &LagError{
		From:     from,
		Oldest:   fmt.Sprintf("%08d:%d", segments[0], 0),
		Segments: pruned,
	}
}

// parseOffset reads a segment and position.
func parseOffset(offset string) (int, int64, error) {
	seg, pos, ok := strings.Cut(offset, ":")
	if !ok {
		return 0, 0, fmt.Errorf("%w: %q", ErrBadOffset, offset)
	}
	segment, err := strconv.Atoi(seg)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %q", ErrBadOffset, offset)
	}
	at, err := strconv.ParseInt(pos, 10, 64)
	if err != nil || at < 0 {
		return 0, 0, fmt.Errorf("%w: %q", ErrBadOffset, offset)
	}
	return segment, at, nil
}

// Resolve turns `earliest`, `latest`, or an offset into a concrete
// starting point.
func (b *Bus) Resolve(from string) (int, int64, error) {
	segments, err := b.segments()
	if err != nil {
		return 0, 0, err
	}
	switch from {
	case "", Latest:
		if len(segments) == 0 {
			return 1, 0, nil
		}
		last := segments[len(segments)-1]
		info, err := os.Stat(b.path(last))
		if err != nil {
			return last, 0, nil
		}
		return last, info.Size(), nil
	case Earliest:
		if len(segments) == 0 {
			return 1, 0, nil
		}
		return segments[0], 0, nil
	}
	return parseOffset(from)
}

// Read returns up to limit events from a starting position, filtered by
// tag globs, and the offset to continue from.
func (b *Bus) Read(from string, tags []string, limit int) ([]Event, string, error) {
	// Before anything is read. A reader that has fallen off the back is
	// told so rather than silently advanced to the oldest segment that
	// still exists — SPEC 17.2, and DIVERGENCE 4.12 for what it used to
	// do instead.
	if err := b.Lag(from); err != nil {
		return nil, "", err
	}
	segment, at, err := b.Resolve(from)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = 200
	}
	segments, err := b.segments()
	if err != nil {
		return nil, "", err
	}

	var out []Event
	next := fmt.Sprintf("%08d:%d", segment, at)
	for _, seq := range segments {
		if seq < segment {
			continue
		}
		start := int64(0)
		if seq == segment {
			start = at
		}
		events, end, err := b.readSegment(seq, start, tags, limit-len(out))
		if err != nil {
			return nil, "", err
		}
		out = append(out, events...)
		next = fmt.Sprintf("%08d:%d", seq, end)
		if len(out) >= limit {
			break
		}
	}
	return out, next, nil
}

// readSegment scans one file from a position.
func (b *Bus) readSegment(seq int, at int64, tags []string, limit int) ([]Event, int64, error) {
	file, err := os.Open(b.path(seq))
	if errors.Is(err, fs.ErrNotExist) {
		// A segment retention removed between listing and reading is
		// not an error; the subscriber has simply fallen behind.
		return nil, at, nil
	}
	if err != nil {
		return nil, at, fmt.Errorf("reading the event bus: %w", err)
	}
	defer file.Close()
	if _, err := file.Seek(at, io.SeekStart); err != nil {
		return nil, at, err
	}

	var out []Event
	pos := at
	reader := bufio.NewReader(file)
	for len(out) < limit {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			pos += int64(len(line))
			var e Event
			if jsonErr := json.Unmarshal(line, &e); jsonErr == nil {
				e.Offset = fmt.Sprintf("%08d:%d", seq, pos)
				if MatchAny(tags, e.Tag) {
					out = append(out, e)
				}
			}
			// A line that will not parse is a truncated write from a
			// crash. It is skipped rather than ending the read: the
			// records after it are still good.
		}
		if err != nil {
			break
		}
	}
	return out, pos, nil
}

// wake releases everything waiting for a record.
func (b *Bus) wake() {
	b.waitMu.Lock()
	waiters := b.waiters
	b.waiters = nil
	b.waitMu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// Wait returns a channel closed when the next record is appended, so a
// follower blocks rather than polling a file that is not changing.
func (b *Bus) Wait() <-chan struct{} {
	ch := make(chan struct{})
	b.waitMu.Lock()
	b.waiters = append(b.waiters, ch)
	b.waitMu.Unlock()
	return ch
}

// Prune enforces retention by age and by total size, whichever binds
// first. A whole segment is the unit: half a file is not a thing a
// reader can be given.
func (b *Bus) Prune() (int, error) {
	segments, err := b.segments()
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	current := b.segment
	b.mu.Unlock()

	now := b.now()
	removed := 0
	var total int64
	type seg struct {
		seq   int
		bytes int64
		mod   time.Time
	}
	var keep []seg
	for _, seq := range segments {
		info, err := os.Stat(b.path(seq))
		if err != nil {
			continue
		}
		// The segment being written is never a candidate.
		if seq == current {
			total += info.Size()
			continue
		}
		if b.Retention > 0 && now.Sub(info.ModTime()) > b.Retention {
			if err := os.Remove(b.path(seq)); err != nil {
				return removed, err
			}
			removed++
			continue
		}
		keep = append(keep, seg{seq: seq, bytes: info.Size(), mod: info.ModTime()})
		total += info.Size()
	}
	for _, s := range keep {
		if b.MaxBytes <= 0 || total <= b.MaxBytes {
			break
		}
		if err := os.Remove(b.path(s.seq)); err != nil {
			return removed, err
		}
		total -= s.bytes
		removed++
	}
	return removed, nil
}
