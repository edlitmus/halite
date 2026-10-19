package keystore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DeletedCert is a certificate whose record was deleted while it could
// still be presented.
//
// Deleting a record used to delete the only thing that named its serial.
// `keys delete` revoked an accepted node first, but the revocation was a
// state on the record it then removed, so nothing was left for
// LoadDenylist to read at the next start -- and nothing for a running
// hub's reconcile loop to read either, since the CLI's denylist is its
// own process's memory. The deleted node's certificate worked until it
// expired. A record that was already revoked lost its serial the same
// way, which made `keys delete` the way to undo a revocation (issue 321,
// M-05 follow-up). These are kept apart from the records because the
// node may enrol again under the same name, and its new record must not
// carry the old certificate's denial with it.
type DeletedCert struct {
	Serial   string    `json:"serial"`
	NodeID   string    `json:"node_id"`
	NotAfter time.Time `json:"not_after"`
	// Reason is what the denylist reports: the revocation's reason for a
	// record that had been revoked, and otherwise that it was deleted.
	Reason  string    `json:"reason"`
	Deleted time.Time `json:"deleted"`
}

// Live reports whether the certificate could still be presented at now,
// as SupersededCert.Live does.
func (d DeletedCert) Live(now time.Time) bool {
	return d.NotAfter.IsZero() || now.Before(d.NotAfter)
}

// ReasonDeleted is the denylist's reason for the serial of a record that
// was deleted without being revoked first.
const ReasonDeleted = "the node's record was deleted"

func (s *Store) deletedDir() string { return filepath.Join(s.dir, "deleted") }

func (s *Store) deletedPath(serial string) (string, error) {
	// The serial is a file name here, so it is held to what
	// pki.SerialString produces rather than trusted.
	if serial == "" || !isHex(evenHex(serial)) {
		return "", fmt.Errorf("%q is not a certificate serial", serial)
	}
	return filepath.Join(s.deletedDir(), serial+".json"), nil
}

// evenHex pads a %x serial, which can have an odd number of digits, so
// that hex.DecodeString will judge its characters rather than its length.
func evenHex(s string) string {
	if len(s)%2 == 1 {
		return "0" + s
	}
	return s
}

// tombstones lists the serials a record names: its own and the ones its
// renewals replaced. Every one is kept, expired or not -- the store has
// no clock, and a reader filters with Live.
func tombstones(rec *Record, now time.Time) []DeletedCert {
	var out []DeletedCert
	if rec.Serial != "" {
		reason := ReasonDeleted
		if rec.State == Revoked {
			reason = reasonOr(rec.Reason, "revoked")
		}
		out = append(out, DeletedCert{Serial: rec.Serial, NodeID: rec.NodeID,
			NotAfter: rec.NotAfter, Reason: reason, Deleted: now})
	}
	for _, sup := range rec.Superseded {
		out = append(out, DeletedCert{Serial: sup.Serial, NodeID: rec.NodeID,
			NotAfter: sup.NotAfter, Reason: ReasonSuperseded, Deleted: now})
	}
	return out
}

func reasonOr(reason, fallback string) string {
	if reason == "" {
		return fallback
	}
	return reason
}

// ListDeleted returns the certificates of deleted records, oldest
// deletion first.
func (s *Store) ListDeleted() ([]DeletedCert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listDeleted()
}

func (s *Store) listDeleted() ([]DeletedCert, error) {
	entries, err := os.ReadDir(s.deletedDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the deleted certificates at %s: %w", s.deletedDir(), err)
	}
	var out []DeletedCert
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.deletedDir(), e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var d DeletedCert
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("the deleted certificate at %s is not readable: %w", path, err)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Deleted.Before(out[j].Deleted) })
	return out, nil
}

func (s *Store) putDeleted(d DeletedCert) error {
	path, err := s.deletedPath(d.Serial)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the deleted certificate %s: %w", d.Serial, err)
	}
	return writeAtomic(path, append(raw, '\n'), 0o600)
}

// pruneDeleted removes the entries whose certificates have expired: the
// handshake refuses them from then on without help. The caller holds the
// lock.
func (s *Store) pruneDeleted(now time.Time) error {
	all, err := s.listDeleted()
	if err != nil {
		return err
	}
	for _, d := range all {
		if d.Live(now) {
			continue
		}
		path, err := s.deletedPath(d.Serial)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("pruning the deleted certificate %s: %w", d.Serial, err)
		}
	}
	return nil
}
