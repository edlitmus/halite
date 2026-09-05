package relay

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultSpoolMax bounds what a relay holds while its upstream is
// away.
//
// Generous, because the thing being held is the estate's evidence that
// its jobs ran, and losing it is the syndic failure SPEC 5.3 names. A
// bound is still needed: a relay whose upstream never comes back must
// not fill the disk of the segment it serves.
const DefaultSpoolMax = 512 << 20

// Spool is the durable store of returns the upstream could not take.
type Spool struct {
	dir string
	max int64

	mu    sync.Mutex
	bytes int64
	// measured records that the directory has been summed once, so
	// every write does not read it.
	measured bool
	// dropped counts returns refused because the spool was full,
	// which an operator needs to see rather than infer.
	dropped int
	// seq orders returns spooled inside one clock tick, and keeps two
	// of them from being the same file. See Put.
	seq uint64
}

// Entry is one spooled return.
type Entry struct {
	Name string
	Body []byte
}

// OpenSpool prepares the directory.
//
// A relay with no spool directory is refused rather than run without
// one: SPEC 5.3's first improvement over the syndic is that an upstream
// outage does not lose returns, and a relay that silently had no spool
// would be a syndic with a better name.
func OpenSpool(dir string, max int64) (*Spool, error) {
	if dir == "" {
		return nil, fmt.Errorf("a relay needs a spool directory; without one an upstream " +
			"outage loses returns, which is the syndic behaviour SPEC 5.3 replaces")
	}
	if max <= 0 {
		max = DefaultSpoolMax
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Spool{dir: dir, max: max}, nil
}

// Put writes one return.
func (s *Spool) Put(body []byte, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.measure(); err != nil {
		return err
	}
	if s.bytes+int64(len(body)) > s.max {
		// Refused rather than making room by deleting the oldest. The
		// oldest return is the one from the start of the outage, which
		// is usually the one somebody wants; and a spool that silently
		// discards is the failure it exists to prevent.
		s.dropped++
		return fmt.Errorf("the relay spool is full at %d bytes", s.bytes)
	}
	// The sequence is what makes this name unique, and that is a
	// stronger requirement here than ordering.
	//
	// The name was the nanosecond timestamp and the drop count. Two
	// returns spooled inside one clock tick produce the same timestamp
	// and the same drop count, so they produce the *same file*, and the
	// second silently overwrote the first -- in the one mechanism whose
	// stated purpose is that an outage delays returns rather than
	// losing them. `time.Now` is only as fine as the platform's clock,
	// and on Windows that is about half a millisecond.
	//
	// Found by inspection while fixing the same root cause in the
	// webhook returner's spool, where a coarse clock reordered the
	// backlog instead of eating it. No test reached this one.
	s.seq++
	name := fmt.Sprintf("%020d-%09d-%d.json", now.UTC().UnixNano(), s.seq, s.dropped)
	if err := os.WriteFile(filepath.Join(s.dir, name), body, 0o600); err != nil {
		return err
	}
	s.bytes += int64(len(body))
	return nil
}

// Next reads the oldest spooled return.
//
// Oldest first, so the upstream sees returns in the order they
// happened rather than in the order the filesystem lists them.
func (s *Spool) Next() (Entry, bool, error) {
	names, err := s.names()
	if err != nil {
		return Entry{}, false, err
	}
	if len(names) == 0 {
		return Entry{}, false, nil
	}
	body, err := os.ReadFile(filepath.Join(s.dir, names[0]))
	if err != nil {
		return Entry{}, false, err
	}
	return Entry{Name: names[0], Body: body}, true, nil
}

// Remove deletes a spooled return that has been delivered.
func (s *Spool) Remove(e Entry) error {
	path := filepath.Join(s.dir, e.Name)
	info, statErr := os.Stat(path)
	if err := os.Remove(path); err != nil {
		return err
	}
	if statErr == nil {
		s.mu.Lock()
		s.bytes -= info.Size()
		if s.bytes < 0 {
			s.bytes = 0
		}
		s.mu.Unlock()
	}
	return nil
}

// Count is how many returns are waiting.
func (s *Spool) Count() int {
	names, err := s.names()
	if err != nil {
		return 0
	}
	return len(names)
}

// Dropped is how many returns the spool refused because it was full.
func (s *Spool) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

func (s *Spool) names() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	// The name begins with a zero-padded nanosecond timestamp, so this
	// is chronological.
	sort.Strings(names)
	return names, nil
}

// measure sums the directory once. The caller holds the lock.
func (s *Spool) measure() error {
	if s.measured {
		return nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	s.bytes, s.measured = total, true
	return nil
}
