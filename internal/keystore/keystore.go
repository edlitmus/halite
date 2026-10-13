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
	// mu serialises read-modify-write. The hub is one process; a second
	// one pointed at the same directory is a misconfiguration that the
	// lock file guards against, not a supported arrangement.
	mu sync.Mutex
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
func (s *Store) Put(rec *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.put(rec)
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
func (s *Store) Delete(nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(nodeID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: %w", nodeID, ErrNotFound)
		}
		return fmt.Errorf("deleting the record for %s: %w", nodeID, err)
	}
	return nil
}

// List returns every record, in node ID order.
func (s *Store) List() ([]*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
