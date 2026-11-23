package fileserver

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/treesign"
)

// SignatureMode is `require_state_signature`.
type SignatureMode int

// The three values. Off is the default and behaves exactly as a node
// without the feature: no request for `_halite/`, no wire change.
const (
	SignatureOff SignatureMode = iota
	SignatureWarn
	SignatureRequired
)

func (m SignatureMode) String() string {
	switch m {
	case SignatureWarn:
		return "warn"
	case SignatureRequired:
		return "true"
	}
	return "false"
}

// ParseSignatureMode reads the setting. A value this build cannot read is
// an error, which the node makes fatal at start: a security control that
// quietly required nothing would be worse than not having one, because an
// operator would believe they had it -- `require_job_signature`'s reason.
func ParseSignatureMode(raw any, ok bool) (SignatureMode, error) {
	if !ok || raw == nil {
		return SignatureOff, nil
	}
	switch t := raw.(type) {
	case bool:
		if t {
			return SignatureRequired, nil
		}
		return SignatureOff, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "false", "":
			return SignatureOff, nil
		case "warn":
			return SignatureWarn, nil
		case "true":
			return SignatureRequired, nil
		}
	}
	return SignatureOff, fmt.Errorf("`require_state_signature` is false, warn or true, not %v", raw)
}

// TreeGuard is a node's check of signed state content, shared by the
// hub's tree (Remote) and this node's own roots (Fetcher).
//
// It lives inside both rather than wrapping them, because the node
// type-asserts its tree to *Remote in three places (a job naming another
// environment, extension synchronization, tracing), and a wrapper would
// fail those assertions and quietly stop verifying the first time a job
// named a second environment. The proposal's section 2.8 is that trap.
type TreeGuard struct {
	Mode SignatureMode
	Keys []jobsign.SignerKey
	// MaxAge refuses a manifest issued longer ago than this, even without
	// an expiry. Zero is no bound.
	MaxAge time.Duration
	// Records holds the high-water marks, under the node's state_dir.
	Records treesign.Store
	// Objects is the content-addressed store of verified bodies, under
	// the node's cache_dir.
	Objects string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Warn and Info are the node's logger. Either may be nil.
	Warn func(msg string, kv ...any)
	Info func(msg string, kv ...any)
	// Refused counts a refusal by reason, and Accepted reports the
	// revision accepted for an environment. Either may be nil.
	Refused  func(reason treesign.Reason)
	Accepted func(env string, issued time.Time)

	// mu serialises acceptance, so two runs that start together cannot
	// both read the old mark and both write theirs.
	mu sync.Mutex
	// process is the pin for a caller that never began a run: a one-shot
	// command, which is one run for the life of its process.
	processOnce sync.Once
	process     *TreeRun
}

// Active reports whether anything is checked.
func (g *TreeGuard) Active() bool { return g != nil && g.Mode != SignatureOff }

func (g *TreeGuard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// NewRun is one run's pin: the first manifest accepted for an environment
// in a run is the one every read of that run is answered from, so a tree
// that changes on the hub mid-run fails the run at the changed file rather
// than giving it files from two revisions.
func NewRun() *TreeRun { return &TreeRun{revs: map[string]runRevision{}} }

// TreeRun is one run's accepted revisions, by environment.
type TreeRun struct {
	mu   sync.Mutex
	revs map[string]runRevision
}

type runRevision struct {
	rev *Revision
	err error
}

// Revision is one accepted manifest.
type Revision struct {
	Manifest *treesign.Manifest
	ID       string
	Signer   string
	// Offline is a revision answered from this node's record because the
	// hub could not be reached: every object it names must already be
	// here.
	Offline bool
}

func (g *TreeGuard) runOr(run *TreeRun) *TreeRun {
	if run != nil {
		return run
	}
	g.processOnce.Do(func() { g.process = NewRun() })
	return g.process
}

// note logs and counts a refusal. In warn mode it is what `true` would
// have refused, said as such.
func (g *TreeGuard) note(r *treesign.Refusal) {
	if g.Refused != nil {
		g.Refused(r.Reason)
	}
	if g.Warn == nil {
		return
	}
	kv := []any{"env", r.Env, "reason", string(r.Reason), "error", r.Error()}
	if r.Path != "" {
		kv = append(kv, "path", r.Path)
	}
	if g.Mode == SignatureWarn {
		g.Warn("signed state content: require_state_signature: true would have refused this; "+
			"applying it as require_state_signature: warn says to", kv...)
		return
	}
	g.Warn("signed state content: refused", kv...)
}

// refuse is the one place the mode decides: in warn mode the caller is
// told to go on as it would without the feature (a nil error and a nil
// revision); with the requirement on, it gets the refusal.
func (g *TreeGuard) refuse(r *treesign.Refusal) error {
	g.note(r)
	if g.Mode == SignatureWarn {
		return nil
	}
	return r
}

// signedFiles fetches an environment's manifest and signature. offline
// says the answer came from this node's record because no hub answered.
type signedFiles func(env string) (manifest, sig []byte, offline bool, err error)

// revision is the run's accepted manifest for an environment, verifying
// and recording it on the run's first read of that environment.
//
// A nil revision with a nil error is warn mode declining to stand in the
// way: the caller reads as it would without the feature.
func (g *TreeGuard) revision(run *TreeRun, env string, load signedFiles) (*Revision, error) {
	run = g.runOr(run)
	run.mu.Lock()
	defer run.mu.Unlock()
	if got, ok := run.revs[env]; ok {
		return got.rev, got.err
	}
	rev, err := g.accept(env, load)
	run.revs[env] = runRevision{rev: rev, err: err}
	return rev, err
}

func (g *TreeGuard) accept(env string, load signedFiles) (*Revision, error) {
	if len(g.Keys) == 0 {
		// Not fatal at start: where this node's configuration is itself
		// managed by the tree, a fatal setting is one only somebody at
		// the console can repair. The node starts, answers exec jobs that
		// read no tree, and refuses every tree read, saying why.
		return nil, g.refuse(treesign.Refuse(treesign.ReasonNoKeys, env, "",
			"this node requires signed state content (require_state_signature) and state_signer_keys "+
				"is empty, so it reads no state tree for environment %s", env))
	}
	manifest, sig, offline, err := load(env)
	if err != nil {
		if offline {
			return nil, g.refuse(treesign.Refuse(treesign.ReasonOffline, env, "",
				"the hub cannot be reached and this node has accepted no signed manifest for environment %s "+
					"to apply offline (require_state_signature): %v", env, err))
		}
		return nil, g.refuse(treesign.Refuse(treesign.ReasonMissing, env, "",
			"the tree serves no signed manifest for environment %s (%s and %s): %v; this node requires "+
				"one (require_state_signature) and read nothing from it",
			env, treesign.ManifestPath, treesign.SignaturePath, err))
	}
	signer, err := treesign.Verify(g.Keys, manifest, sig)
	if err != nil {
		return nil, g.refuse(treesign.Refuse(treesign.ReasonSignature, env, "",
			"the manifest for environment %s: %v", env, err))
	}
	m, err := treesign.Parse(manifest)
	if err != nil {
		return nil, g.refuse(treesign.Refuse(treesign.ReasonMalformed, env, "",
			"the manifest for environment %s verifies and is refused: %v", env, err))
	}
	id := treesign.ID(manifest)

	g.mu.Lock()
	defer g.mu.Unlock()
	prev, err := g.Records.Load(env)
	if err != nil {
		return nil, g.refuse(treesign.Refuse(treesign.ReasonRecord, env, "",
			"this node's record of the last manifest accepted for %s cannot be read: %v", env, err))
	}
	if err := treesign.CheckFresh(m, id, env, g.now(), g.MaxAge, prev); err != nil {
		var refusal *treesign.Refusal
		if errors.As(err, &refusal) {
			return nil, g.refuse(refusal)
		}
		return nil, err
	}
	rev := &Revision{Manifest: m, ID: id, Signer: signer, Offline: offline}
	if prev == nil || prev.ID != id {
		// Recorded, and synced, before any file of the revision is read.
		record := treesign.Record{Env: env, Issued: m.Issued, ID: id, Signer: signer,
			Accepted: treesign.FormatTime(g.now()), Source: m.Source}
		if err := g.Records.Save(record, manifest, sig); err != nil {
			// Refused whatever the mode: a mark that was not written is a
			// rollback that is not stopped at the next start.
			return nil, treesign.Refuse(treesign.ReasonRecord, env, "",
				"the manifest for %s verifies and could not be recorded, so it was not used: %v", env, err)
		}
		if g.Info != nil {
			old := ""
			if prev != nil {
				old = prev.Issued
			}
			g.Info("signed state content: accepted a new revision", "env", env, "issued", m.Issued,
				"previous_issued", old, "manifest", id, "signer", signer, "source", m.Source)
		}
		g.collect()
	}
	if g.Accepted != nil {
		g.Accepted(env, m.IssuedAt())
	}
	return rev, nil
}

// objectPath is where a verified body lives:
// <objects>/<hash type>/<first two hex>/<digest>/<basename>.
//
// The basename stays at the end because `archive.extracted` picks an
// archive's format from the local path's suffix, and a name that was only
// a digest would make every archive look like a tarball.
func (g *TreeGuard) objectPath(rev *Revision, e treesign.Entry) (string, error) {
	if g.Objects == "" {
		return "", errors.New("this node has no cache directory for verified state content")
	}
	return filepath.Join(g.Objects, rev.Manifest.HashType, e.Hash[:2], e.Hash, path.Base(e.Path)), nil
}

// haveObject reports whether the verified body is already here, hashing
// it once per process through the shared digest cache.
func (g *TreeGuard) haveObject(cache *digestCache, rev *Revision, e treesign.Entry) (string, bool) {
	p, err := g.objectPath(rev, e)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(p)
	if err != nil || info.Size() != e.Size {
		return p, false
	}
	key := rev.Manifest.HashType + ":" + p
	cache.mu.Lock()
	known := cache.by[key]
	cache.mu.Unlock()
	if known == e.Hash {
		return p, true
	}
	digest, err := hashOf(p, rev.Manifest.HashType)
	if err != nil || digest != e.Hash {
		// Something other than this node changed it. Removed, so that the
		// next read fetches the signed body again.
		_ = os.Remove(p)
		return p, false
	}
	cache.mu.Lock()
	cache.by[key] = digest
	cache.mu.Unlock()
	return p, true
}

// keep checks a body against the manifest and stores it.
//
// The hub's own digest for the body is not consulted: a compromised hub
// publishes a digest for whatever it sends. It appears only in the
// diagnostic.
func (g *TreeGuard) keep(cache *digestCache, env string, rev *Revision, e treesign.Entry, body []byte, published string) (string, error) {
	got, err := digestOf(body, rev.Manifest.HashType)
	if err != nil {
		return "", err
	}
	if got != e.Hash {
		hub := ""
		if published != "" {
			hub = fmt.Sprintf(" (the hub published %s)", published)
		}
		return "", treesign.Refuse(treesign.ReasonDigest, env, e.Path,
			"%s/%s arrived with %s %s%s and revision %s (issued %s, signed by %s) says %s; it was not used",
			env, e.Path, rev.Manifest.HashType, got, hub, rev.ID, rev.Manifest.Issued, rev.Signer, e.Hash)
	}
	p, err := g.objectPath(rev, e)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(p, body, 0o600); err != nil {
		return "", err
	}
	cache.mu.Lock()
	cache.by[rev.Manifest.HashType+":"+p] = e.Hash
	cache.mu.Unlock()
	return p, nil
}

// notInRevision is the answer for a path the signer never published.
func notInRevision(env, rel string, rev *Revision) *treesign.Refusal {
	return &treesign.Refusal{Reason: treesign.ReasonNotInManifest, Env: env, Path: rel,
		Err: fmt.Errorf("%w: %s is not in the signed revision %s of environment %s (issued %s, signed by %s)",
			fs.ErrNotExist, rel, rev.ID, env, rev.Manifest.Issued, rev.Signer)}
}

// listing answers a listing from the signed manifest, never from the hub,
// so the hub can neither add a file to a `file.recurse` nor hide one.
// The prefix is matched as Roots.Manifest matches it, and relativeTo cuts
// it on a path boundary after.
func listing(env, prefix string, rev *Revision) *Manifest {
	prefix = strings.TrimPrefix(prefix, "/")
	out := &Manifest{Env: env, Prefix: prefix, HashType: rev.Manifest.HashType}
	for _, e := range rev.Manifest.Files {
		if prefix != "" && !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		out.Files = append(out.Files, Entry{Path: e.Path, Hash: e.Hash, Size: e.Size})
	}
	return out
}

// collect removes objects that no environment's current or previous
// accepted manifest names.
//
// Run when a new revision is accepted rather than after each run: an exec
// job still reading the previous revision keeps its objects, and anything
// older than that has no reader. Any record that will not read stops the
// collection, because removing what it names is the wrong direction to
// fail in.
func (g *TreeGuard) collect() {
	if g.Objects == "" {
		return
	}
	records, err := g.Records.Records()
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, r := range records {
		current, _, err := g.Records.Saved(r.Env)
		if err != nil {
			return
		}
		raws := [][]byte{current}
		if previous, err := g.Records.Previous(r.Env); err == nil {
			raws = append(raws, previous)
		}
		for _, raw := range raws {
			m, err := treesign.Parse(raw)
			if err != nil {
				return
			}
			for _, e := range m.Files {
				keep[m.HashType+"/"+e.Hash] = true
			}
		}
	}
	types, err := os.ReadDir(g.Objects)
	if err != nil {
		return
	}
	for _, t := range types {
		fans, _ := os.ReadDir(filepath.Join(g.Objects, t.Name()))
		for _, fan := range fans {
			digests, _ := os.ReadDir(filepath.Join(g.Objects, t.Name(), fan.Name()))
			for _, d := range digests {
				if !keep[t.Name()+"/"+d.Name()] {
					_ = os.RemoveAll(filepath.Join(g.Objects, t.Name(), fan.Name(), d.Name()))
				}
			}
		}
	}
}

// signedRefusal reports whether an error is a refusal of signed content
// other than "not in the revision", which a caller searching candidate
// names treats as not found.
func signedRefusal(err error) bool {
	reason := treesign.ReasonOf(err)
	return reason != "" && reason != treesign.ReasonNotInManifest
}
