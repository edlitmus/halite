package treesign

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/fileperm"
)

// Reason is why a manifest, or a read under one, was refused. It is the
// `reason` label of halite_node_tree_refusals_total, so the set is closed.
type Reason string

// The reasons, in the order a node checks them.
const (
	ReasonNoKeys        Reason = "no_keys"
	ReasonMissing       Reason = "missing"
	ReasonSignature     Reason = "signature"
	ReasonMalformed     Reason = "malformed"
	ReasonEnv           Reason = "env"
	ReasonExpired       Reason = "expired"
	ReasonAge           Reason = "age"
	ReasonFuture        Reason = "future"
	ReasonRollback      Reason = "rollback"
	ReasonEquivocation  Reason = "equivocation"
	ReasonRecord        Reason = "record"
	ReasonNotInManifest Reason = "not_in_manifest"
	ReasonDigest        Reason = "digest"
	ReasonOffline       Reason = "offline"
)

// Reasons is every reason, for declaring the metric's series at zero.
var Reasons = []Reason{ReasonNoKeys, ReasonMissing, ReasonSignature, ReasonMalformed, ReasonEnv,
	ReasonExpired, ReasonAge, ReasonFuture, ReasonRollback, ReasonEquivocation, ReasonRecord,
	ReasonNotInManifest, ReasonDigest, ReasonOffline}

// Refusal is a refusal with its reason, so a caller can count it.
type Refusal struct {
	Reason Reason
	Env    string
	Path   string
	Err    error
}

func (r *Refusal) Error() string { return r.Err.Error() }
func (r *Refusal) Unwrap() error { return r.Err }

// Refuse builds one.
func Refuse(reason Reason, env, path string, format string, args ...any) *Refusal {
	return &Refusal{Reason: reason, Env: env, Path: path, Err: fmt.Errorf(format, args...)}
}

// ReasonOf is the reason an error carries, or "" for one that is not a
// refusal.
func ReasonOf(err error) Reason {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Reason
	}
	return ""
}

// Skew is the clock difference tolerated between the signer and a node,
// as for a signed job's expiry and for `doctor`'s clock check.
const Skew = time.Minute

// Record is an environment's high-water mark: the newest manifest this
// node has accepted for it.
type Record struct {
	Env      string `json:"env"`
	Issued   string `json:"issued"`
	ID       string `json:"manifest"`
	Signer   string `json:"signer"`
	Accepted string `json:"accepted"`
	Source   string `json:"source,omitempty"`
}

// CheckFresh is steps 4 to 6 of the proposal's verification: the
// environment, the expiry and the age, and the high-water mark.
//
// A pure function of the manifest, the clock and the stored record, so
// each rule is tested without a hub.
func CheckFresh(m *Manifest, id, env string, now time.Time, maxAge time.Duration, prev *Record) error {
	if m.Env != env {
		// Or a compromised hub serves the `dev` environment's validly
		// signed manifest and content as `base`.
		return Refuse(ReasonEnv, env, "",
			"the hub served a manifest signed for environment %q as environment %q; refusing it", m.Env, env)
	}
	if !m.expires.IsZero() && now.After(m.expires.Add(Skew)) {
		return Refuse(ReasonExpired, env, "",
			"the manifest for %s expired at %s; sign the tree again", env, m.Expires)
	}
	if maxAge > 0 && now.Sub(m.issued) > maxAge+Skew {
		return Refuse(ReasonAge, env, "",
			"the manifest for %s was issued %s, %s ago, and this node accepts one at most %s old "+
				"(state_manifest_max_age); sign the tree again", env, m.Issued,
			now.Sub(m.issued).Round(time.Second), maxAge)
	}
	if m.issued.After(now.Add(Skew)) {
		// A manifest from the future would become the high-water mark
		// and make every honest manifest after it look like a rollback
		// until the clock caught up with it.
		return Refuse(ReasonFuture, env, "",
			"the manifest for %s was issued %s, which is %s in this node's future; check the signer's clock",
			env, m.Issued, m.issued.Sub(now).Round(time.Second))
	}
	if prev == nil {
		return nil
	}
	previous, err := time.Parse(time.RFC3339Nano, prev.Issued)
	if err != nil {
		return Refuse(ReasonRecord, env, "", "this node's record of the last manifest for %s is not readable: %v", env, err)
	}
	switch {
	case m.issued.Before(previous):
		return Refuse(ReasonRollback, env, "",
			"the manifest for %s was issued %s and this node has already accepted one issued %s; "+
				"refusing an older revision", env, m.Issued, prev.Issued)
	case m.issued.Equal(previous) && id != prev.ID:
		return Refuse(ReasonEquivocation, env, "",
			"the manifest for %s (%s) has the same issued time, %s, as the one this node accepted (%s) "+
				"and different content; two revisions signed for one instant mean a signer misbehaving "+
				"or a key in two hands", env, id, m.Issued, prev.ID)
	}
	return nil
}

// Store keeps the high-water marks, and the manifest each one names, so
// that an outage can apply the last accepted revision whole.
//
// `<dir>/<env>.json` is the record; `<env>.manifest.json` and
// `<env>.manifest.sig` are the bytes it was accepted from, and
// `<env>.previous.manifest.json` the one before, for the object store's
// collection. The record is written last, so it is the commit point.
type Store struct {
	Dir string
}

func (s Store) path(env, suffix string) (string, error) {
	if err := CheckEnv(env); err != nil {
		return "", err
	}
	if s.Dir == "" {
		return "", errors.New("no directory for the tree revision records")
	}
	return filepath.Join(s.Dir, env+suffix), nil
}

// Load reads an environment's record, nil when there is none.
func (s Store) Load(env string) (*Record, error) {
	p, err := s.path(env, ".json")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return &r, nil
}

// Saved is the manifest and signature the record was accepted from.
func (s Store) Saved(env string) ([]byte, []byte, error) {
	mp, err := s.path(env, ".manifest.json")
	if err != nil {
		return nil, nil, err
	}
	sp, _ := s.path(env, ".manifest.sig")
	m, err := os.ReadFile(mp)
	if err != nil {
		return nil, nil, err
	}
	sig, err := os.ReadFile(sp)
	if err != nil {
		return nil, nil, err
	}
	return m, sig, nil
}

// Previous is the manifest accepted before the current one, if kept.
func (s Store) Previous(env string) ([]byte, error) {
	p, err := s.path(env, ".previous.manifest.json")
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Save makes a record the high-water mark, synced before it returns.
//
// It is written before any file of the revision is read, the discipline
// the signed-job replay record follows: a node that crashed between the
// two must come back on the new mark, or a rollback could be slipped in
// at the restart.
func (s Store) Save(r Record, manifest, sig []byte) error {
	mp, err := s.path(r.Env, ".manifest.json")
	if err != nil {
		return err
	}
	if err := fileperm.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if old, err := os.ReadFile(mp); err == nil && ID(old) != r.ID {
		prev, _ := s.path(r.Env, ".previous.manifest.json")
		if err := atomicfile.Write(prev, old, 0o600); err != nil {
			return err
		}
	}
	if err := atomicfile.Write(mp, manifest, 0o600); err != nil {
		return err
	}
	sp, _ := s.path(r.Env, ".manifest.sig")
	if err := atomicfile.Write(sp, sig, 0o600); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	rp, _ := s.path(r.Env, ".json")
	return atomicfile.Write(rp, append(raw, '\n'), 0o600)
}

// Records lists every environment with a record, for `doctor`.
func (s Store) Records() ([]Record, error) {
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".manifest.json") {
			continue
		}
		env := strings.TrimSuffix(name, ".json")
		r, err := s.Load(env)
		if err != nil {
			return nil, err
		}
		if r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}
