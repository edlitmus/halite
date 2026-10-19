// Package keystore is the hub's record of who has asked to join and who
// has been let in: SPEC section 7.4's five states, on disk.
//
// One JSON file per node, named for the node, written atomically. A
// directory rather than a database because the thing an operator does at
// three in the morning is read it, and because a key store that cannot
// be recovered with `cat` is a key store that gets recovered by
// guessing.
package keystore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/pki"
)

// State is a key's position in the lifecycle of SPEC 7.4.
type State string

const (
	// Pending is a request received and not yet decided.
	Pending State = "pending"
	// Accepted is a certificate issued and valid.
	Accepted State = "accepted"
	// Rejected is an explicit refusal. The request is retained, because
	// the question an audit asks later is what was refused.
	Rejected State = "rejected"
	// Revoked is an acceptance withdrawn. The serial goes on the
	// denylist and stays in the record.
	Revoked State = "revoked"
	// Expired is a certificate past notAfter. It is derived from the
	// clock rather than stored, so a hub that was switched off does not
	// come back believing a year-old certificate is current.
	Expired State = "expired"
)

// States lists the lifecycle in the order an operator reads it.
var States = []State{Pending, Accepted, Rejected, Revoked, Expired}

// ValidState reports whether s is a state a record may be stored in.
// Expired is excluded: it is computed, never written.
func ValidState(s State) bool {
	switch s {
	case Pending, Accepted, Rejected, Revoked:
		return true
	}
	return false
}

// Source is how a node came to be enrolled, per SPEC 7.3.
type Source string

const (
	SourceManual   Source = "manual"
	SourceToken    Source = "token"
	SourceAttested Source = "attested"
)

// Record is one node's entry.
//
// The certificate request is kept whatever the outcome, so that a
// rejection can be explained and an acceptance can be re-derived.
type Record struct {
	NodeID      string    `json:"node_id"`
	State       State     `json:"state"`
	Source      Source    `json:"source"`
	Fingerprint string    `json:"fingerprint"`
	CSR         string    `json:"csr,omitempty"`
	Cert        string    `json:"cert,omitempty"`
	Serial      string    `json:"serial,omitempty"`
	NotAfter    time.Time `json:"not_after,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	FirstSeen   time.Time `json:"first_seen"`
	Updated     time.Time `json:"updated"`
	// TokenID names the bootstrap token that admitted this node, when
	// one did, so that revoking a leaked token can name what it let in.
	TokenID string `json:"token_id,omitempty"`
	// Superseded holds the serials renewals replaced, for as long as
	// their certificates would otherwise still be valid. The record
	// used to keep only the current serial, so the denylist entry a
	// renewal made for the old one lived in the hub's memory alone: a
	// restart forgot it, and a key rotated because it leaked worked
	// again for the rest of its life (issue 321, M-05). Keeping them
	// here is what lets LoadDenylist and the hub's reconcile loop put
	// them back. An entry is dropped once its certificate has expired,
	// because the handshake refuses it from then on regardless.
	Superseded []SupersededCert `json:"superseded,omitempty"`
}

// SupersededCert is a certificate a renewal replaced before it expired.
type SupersededCert struct {
	Serial   string    `json:"serial"`
	NotAfter time.Time `json:"not_after"`
	// At is when it was replaced.
	At time.Time `json:"at"`
}

// Live reports whether the certificate could still be presented at now:
// whether it still belongs on the denylist. An unknown notAfter is
// treated as live, so a record written without one fails closed.
func (s SupersededCert) Live(now time.Time) bool {
	return s.NotAfter.IsZero() || now.Before(s.NotAfter)
}

// Status is the record's state with the clock applied: an accepted
// certificate past its notAfter reports expired.
func (r *Record) Status(now time.Time) State {
	if r.State == Accepted && !r.NotAfter.IsZero() && now.After(r.NotAfter) {
		return Expired
	}
	return r.State
}

// ErrNotFound is returned for a node the hub has never heard from.
var ErrNotFound = errors.New("no such node in the key store")

// Store is a directory of records.
type Store struct {
	dir string
	// mu serialises this Store's own goroutines. It does nothing about
	// another process, and there always is one: `halite-hub keys
	// accept|reject|revoke|delete` and `keys token` open the same
	// directory from the operator's shell while the hub is serving. This
	// comment used to say a lock file guarded against a second process.
	// There was no lock file, so a revocation from the CLI could be
	// overwritten by the hub renewing the same node a moment later, and
	// the node was left accepted with a fresh serial that no denylist
	// had ever seen. DIVERGENCE 5.323. Every read-modify-write now runs
	// inside locked, which takes mu and then an exclusive OS lock on
	// lockName, so the exclusion is the same between processes as
	// between goroutines.
	mu sync.Mutex

	// between, when set, runs inside each read-modify-write's critical
	// section, after the record is read and before it is written: the
	// window in which, before DIVERGENCE 5.323, another process's
	// decision could land and then be overwritten. It lets a test put a
	// second Store over the same directory -- which is what the hub and
	// `halite-hub keys` are -- into exactly that window, instead of
	// hoping a loop finds it. Nil in production.
	between func(op, id string)
}

func (s *Store) inWindow(op, id string) {
	if s.between != nil {
		s.between(op, id)
	}
}

// lockName is the file in the store's directory that every process
// writing the store locks. It is never written to: it exists to be
// locked, and the OS drops the lock when the holder exits, so a process
// that dies holding it leaves nothing to clean up.
const lockName = ".lock"

// locked runs fn holding the store against every other writer, in this
// process and in any other. fn may read and write records and tokens
// through the unexported get/put/getToken/putToken, which take no lock
// of their own.
//
// The lock covers the token directory as well as the records: it is
// one store to an operator, and SpendToken and RevokeToken have the
// same read-modify-write shape as Revoke and Renew.
func (s *Store) locked(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.openLock()
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("locking the key store at %s: %w", s.dir, err)
	}
	defer unlockFile(f)
	return fn()
}

// tryLocked reports whether the store's lock could be taken at this
// moment, and releases it if it was. It is for the tests, which use it
// to show -- without timing anything -- that another Store, in this
// process or another, holds the lock.
func (s *Store) tryLocked() (bool, error) {
	f, err := s.openLock()
	if err != nil {
		return false, err
	}
	defer f.Close()
	ok, err := tryLockFile(f)
	if ok {
		unlockFile(f)
	}
	return ok, err
}

// openLock opens the lock file, creating it if this is the first write
// the store has seen. Read-write, because Solaris's fcntl lock needs a
// descriptor open for writing to take a write lock.
//
// It is handed to the directory's owner as every record is (#317,
// DIVERGENCE 5.285): `keys revoke` run as root is quite likely to be
// the first writer, and a root-owned 0600 lock file would make every
// write the hub's own account tried afterwards fail at the open.
//
// Through fileperm.OpenFile rather than os.OpenFile, so that 0600 is
// carried out as an ACL on Windows rather than becoming the read-only
// attribute; internal/fileperm's audit refused the os.OpenFile this
// first shipped with, on every CI leg.
func (s *Store) openLock() (*os.File, error) {
	path := filepath.Join(s.dir, lockName)
	f, err := fileperm.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the key store's lock at %s: %w", path, err)
	}
	if err := inheritOwner(path, s.dir); err != nil {
		f.Close()
		return nil, fmt.Errorf("handing the key store's lock at %s to its directory's owner: %w", path, err)
	}
	return f, nil
}

// Open prepares a store, creating the directory if it is absent.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("the key store needs a directory")
	}
	// 0700: the requests are not secret, but who has asked to join is
	// not public either.
	if err := mkdirOwned(dir); err != nil {
		return nil, fmt.Errorf("creating the key store: %w", err)
	}
	return &Store{dir: dir}, nil
}

// mkdirOwned creates dir and any parents it is missing, each belonging to
// the account that owns the directory above it, and sets right a dir that
// an earlier run left owned by someone else.
//
// Files in the store already take their directory's owner (inheritOwner),
// which is correct, and is exactly what made a directory created wrongly
// into a trap: `halite-hub keys token create` run as root made
// state_dir/keys/tokens as root:root 0700 inside the hub account's `keys`,
// every token file written into it then inherited root, and the hub could
// read none of them. Every token enrolment was refused with `permission
// denied` in the hub's log and nothing at all on the node. #317, DIVERGENCE
// 5.285.
//
// Parents are handled outermost first so each takes its owner from a parent
// that is already right. It changes nothing unless this process is root and
// the parent belongs to someone else.
func mkdirOwned(dir string) error {
	var made []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		}
		made = append(made, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	if err := fileperm.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for i := len(made) - 1; i >= 0; i-- {
		if err := inheritOwner(made[i], filepath.Dir(made[i])); err != nil {
			return err
		}
	}
	if len(made) == 0 {
		return inheritOwner(dir, filepath.Dir(dir))
	}
	return nil
}

// CheckTokens reports whether the token store can be read in full, which is
// what the hub needs before it can admit a node with a token. It returns nil
// when there are no tokens yet.
func (s *Store) CheckTokens() error {
	_, err := s.ListTokens()
	return err
}

// Dir is where the store lives, for a message that names it.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(nodeID string) (string, error) {
	if err := pki.ValidateNodeID(nodeID); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, nodeID+".json"), nil
}

// Get returns one record.
func (s *Store) Get(nodeID string) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(nodeID)
}

func (s *Store) get(nodeID string) (*Record, error) {
	path, err := s.path(nodeID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", nodeID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the record for %s: %w", nodeID, err)
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("the record for %s at %s is not readable: %w", nodeID, path, err)
	}
	// A record whose file name and contents disagree is a record that
	// was moved by hand; the file name is the one the store indexes by.
	if rec.NodeID != nodeID {
		return nil, fmt.Errorf("the record at %s names %q, not %q", path, rec.NodeID, nodeID)
	}
	return &rec, nil
}

// Put writes a record, replacing any earlier one.
//
// It takes the store's lock, but only around the write: a caller that
// read the record first and decides from it has a read-modify-write
// that Put cannot protect. The Authority's decisions go through locked
// for that reason, and so should anything else that decides.
func (s *Store) Put(rec *Record) error {
	return s.locked(func() error { return s.put(rec) })
}

func (s *Store) put(rec *Record) error {
	if !ValidState(rec.State) {
		return fmt.Errorf("%q is not a state a record can be stored in", rec.State)
	}
	path, err := s.path(rec.NodeID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the record for %s: %w", rec.NodeID, err)
	}
	return writeAtomic(path, append(raw, '\n'), 0o600)
}

// Delete removes a record entirely. SPEC 7.4 lists it as an operator
// action; it is the only one that loses the audit trail, so the caller
// is expected to have said so out loud.
//
// The serials the record names are written to the deleted certificates
// first, in the same critical section, so that no caller can delete a
// record and lose a certificate that still works. Authority.Delete is
// what the hub and the CLI call, because it also denylists them in this
// process; this is the floor under it.
func (s *Store) Delete(nodeID string) error {
	return s.locked(func() error {
		_, err := s.delete(nodeID, time.Now())
		return err
	})
}

// delete is Delete for a caller that holds the lock. It returns what it
// kept.
func (s *Store) delete(nodeID string, now time.Time) ([]DeletedCert, error) {
	path, err := s.path(nodeID)
	if err != nil {
		return nil, err
	}
	rec, err := s.get(nodeID)
	if err != nil {
		return nil, err
	}
	kept := tombstones(rec, now)
	if len(kept) > 0 {
		if err := mkdirOwned(s.deletedDir()); err != nil {
			return nil, fmt.Errorf("creating the deleted certificates directory: %w", err)
		}
	}
	for _, d := range kept {
		if err := s.putDeleted(d); err != nil {
			return nil, err
		}
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", nodeID, ErrNotFound)
		}
		return nil, fmt.Errorf("deleting the record for %s: %w", nodeID, err)
	}
	return kept, nil
}

// countPending counts the pending records. The caller holds the lock.
func (s *Store) countPending() (int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("reading the key store at %s: %w", s.dir, err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		rec, err := s.get(e.Name()[:len(e.Name())-len(".json")])
		if err != nil {
			return 0, err
		}
		if rec.State == Pending {
			n++
		}
	}
	return n, nil
}

// List returns every record, in node ID order.
func (s *Store) List() ([]*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list()
}

// list is List for a caller that holds the lock.
func (s *Store) list() ([]*Record, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("reading the key store at %s: %w", s.dir, err)
	}
	var out []*Record
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		nodeID := e.Name()[:len(e.Name())-len(".json")]
		rec, err := s.get(nodeID)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out, nil
}

// writeAtomic writes a store record, owned by the account the store
// directory belongs to.
//
// The ownership step has to happen before the rename -- otherwise the
// record is briefly reachable at its final path by the wrong account, and
// is then left owned by the wrong one -- which is why this goes through
// atomicfile.WritePrepared rather than atomicfile.Write.
//
// It used to be a copy of the whole of atomicfile.Write with that one step
// added, and the copy had drifted: it chmod'd the temporary file directly
// instead of going through internal/fileperm, so a record written 0600 on
// Windows got the read-only attribute and its directory's inherited ACL,
// and it never synced the directory after the rename. DIVERGENCE 5.144.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	return atomicfile.WritePrepared(path, data, mode, func(tmp string) error {
		return inheritOwner(tmp, filepath.Dir(path))
	})
}

// InheritOwner gives a file its directory's owner and group, when this
// process is root and the directory belongs to someone else. It is
// inheritOwner, for a file outside the store that a root command writes
// and the hub's own account must read: the policy that
// `keys operator create --admin` writes is the one there is.
func InheritOwner(path, dir string) error { return inheritOwner(path, dir) }
