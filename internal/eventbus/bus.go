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

func (b *Bus) openCurrent() error {
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
