package keystore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/pki"
)

// The pending index is how the hub knows how many requests are pending
// without reading every record in the store.
//
// It used to read every record. Enroll asks before it writes a pending
// record for a name it has not seen (roomForPending, the bound of issue
// 321's M-06), and the answer came from opening and parsing all of them,
// under the store's lock. The comment on it said the cost was "paid only
// for a name not seen before", which was true and was the problem: an
// enrolment storm, and a fleet growing, are made of exactly those names.
// `simfleet setup` issued 5,000 identities at 20, 39, 56, 75 and 93
// seconds per thousand on AWS, rising in a straight line with the store
// (DIVERGENCE 5.279); this package's BenchmarkIssueIntoAStoreOf measured
// 131, 417 and 936 ms per identity into stores of 500, 2,000 and 8,000 on
// a Mac, and its CPU profile put 87% of the run in this count, almost all
// of it in open(2). With the index it is about 34, 37 and 45 ms. DIVERGENCE
// 5.635.
//
// The index is a directory with one empty file per pending record, named
// for the node. Counting it reads names and stops at the limit, so it
// costs at most MaxPending directory entries however large the store is,
// and parses nothing.
//
// It is an index, not a second record: the record is still the
// authority on a node's state, and the index is kept to one rule that
// survives a crash at any point --
//
//	**every pending record has a marker.**
//
// A marker is written before a record becomes pending and removed after
// a record stops being pending (or is deleted). A crash between the two
// steps leaves a marker for a record that is not pending: an overcount,
// never an undercount. An overcount cannot let the bound be exceeded,
// and it cannot wrongly refuse either, because the count is only trusted
// below the limit. At the limit, every marker is checked against its
// record and the stale ones are removed, so a refusal is always made on
// the exact number -- the same answer the full scan gave, from a read of
// at most MaxPending records rather than all of them, and only on the
// path that is about to refuse.
//
// What the rule cannot cover is a pending record that arrived without
// going through put: a record file copied into the store by hand, or
// written by a halite older than the index after the index was built
// (a downgrade, and then an upgrade back). Those are not counted until
// they are decided, which loosens the bound by however many there are.
// The bound is against an unauthenticated caller filling the disk, and
// neither of those is one.
const pendingIndexName = "pending-index"

// pendingBuildName is where the index is assembled before it is renamed
// into place, so that a crash part-way through a build leaves no index at
// all -- which is rebuilt -- rather than a partial one, which would
// undercount.
const pendingBuildName = "pending-index.building"

func (s *Store) pendingDir() string { return filepath.Join(s.dir, pendingIndexName) }

// markPending records that nodeID is about to be pending. The caller
// holds the lock and calls it before the record is written. With no index
// yet there is nothing to do: the build that makes the index reads every
// record, this one included.
func (s *Store) markPending(nodeID string) error {
	marker := filepath.Join(s.pendingDir(), nodeID)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if _, err := os.Stat(s.pendingDir()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	// Through writeAtomic, for the owner and the directory sync. A marker
	// lost to a power cut while its record survived would be the one
	// undercount the rule above forbids.
	if err := writeAtomic(marker, nil, 0o600); err != nil {
		return fmt.Errorf("indexing %s as pending: %w", nodeID, err)
	}
	return nil
}

// unmarkPending removes nodeID's marker, after its record has stopped
// being pending. A failure is not returned: the record is already
// written, and a marker left behind is an overcount, which the check at
// the limit removes. Reporting it would fail an accept or a delete that
// has happened.
func (s *Store) unmarkPending(nodeID string) {
	_ = os.Remove(filepath.Join(s.pendingDir(), nodeID))
}

// countPending reports how many records are pending, for a bound of
// limit. Below limit the answer is the number of markers, which is never
// fewer than the pending records; at or above it, it is exact. So
// `countPending(limit) >= limit` is true exactly when the pending records
// number limit or more. The caller holds the lock.
func (s *Store) countPending(limit int) (int, error) {
	if err := s.ensurePendingIndex(); err != nil {
		return 0, err
	}
	names, err := s.pendingMarkers(limit)
	if err != nil {
		return 0, err
	}
	if len(names) < limit {
		return len(names), nil
	}
	return s.verifyPending()
}

// pendingMarkers reads the index's marker names, stopping once it has
// limit of them; limit <= 0 reads them all. Anything that is not a node
// ID -- an atomic write's temporary file, which starts with a dot -- is
// skipped.
func (s *Store) pendingMarkers(limit int) ([]string, error) {
	f, err := os.Open(s.pendingDir())
	if err != nil {
		return nil, fmt.Errorf("reading the pending index at %s: %w", s.pendingDir(), err)
	}
	defer f.Close()
	var out []string
	for limit <= 0 || len(out) < limit {
		batch, err := f.Readdirnames(256)
		for _, name := range batch {
			if pki.ValidateNodeID(name) == nil {
				out = append(out, name)
			}
		}
		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the pending index at %s: %w", s.pendingDir(), err)
		}
	}
	return out, nil
}

// verifyPending checks every marker against its record, removes those
// whose record is gone or no longer pending, and returns the exact count.
// It reads one record per marker, which is MaxPending reads and some
// stale ones, not the store.
func (s *Store) verifyPending() (int, error) {
	names, err := s.pendingMarkers(0)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range names {
		rec, err := s.get(id)
		switch {
		case errors.Is(err, ErrNotFound):
			s.unmarkPending(id)
		case err != nil:
			return 0, err
		case rec.State != Pending:
			s.unmarkPending(id)
		default:
			n++
		}
	}
	return n, nil
}

// ensurePendingIndex builds the index if the store has none: a store
// written before it existed, or one where a build was interrupted. It
// reads every record, once, and is the only remaining path that does.
func (s *Store) ensurePendingIndex() error {
	if _, err := os.Stat(s.pendingDir()); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading the pending index at %s: %w", s.pendingDir(), err)
	}
	build := filepath.Join(s.dir, pendingBuildName)
	if err := os.RemoveAll(build); err != nil {
		return fmt.Errorf("clearing an interrupted build of the pending index at %s: %w", build, err)
	}
	if err := mkdirOwned(build); err != nil {
		return fmt.Errorf("building the pending index: %w", err)
	}
	recs, err := s.list()
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.State != Pending {
			continue
		}
		if err := writeAtomic(filepath.Join(build, rec.NodeID), nil, 0o600); err != nil {
			return fmt.Errorf("building the pending index: %w", err)
		}
	}
	// A rename lost to a power cut leaves no index, which is rebuilt; the
	// build directory never stands in for one. Through atomicfile.Rename,
	// which on Windows waits out another handle on either path rather than
	// failing the enrolment that happened to be first after an upgrade.
	if err := atomicfile.Rename(build, s.pendingDir()); err != nil {
		return fmt.Errorf("building the pending index: %w", err)
	}
	return nil
}
