package gitfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Backend serves one or more git remotes as file server roots.
type Backend struct {
	opts Options

	mu sync.Mutex
	// roots maps an environment to the directories serving it, in
	// remote order — so a remote listed first shadows a later one for
	// the same path, which is how `roots` already behaves.
	roots map[string][]string
	// state records what each remote resolved to, for the report.
	state []RemoteState
	// updated is when the last successful update finished.
	updated time.Time
	// byRemote is what each remote served at the last update, keyed by its
	// mirror, so that a remote that fails keeps its last good roots. The
	// ref is kept with the directory, not just the directory, because a
	// kept tree is verified again before it is carried forward and the
	// verification needs the object id and whether it was a tag.
	byRemote map[string][]servedRef
}

// servedRef is one tree a remote is serving, and the ref it came from.
type servedRef struct {
	Ref Ref
	Dir string
}

// RemoteState is what one remote resolved to at the last update.
type RemoteState struct {
	Remote Remote
	Refs   []Ref
	// Err is why this remote contributed nothing.
	Err error
	// Refused lists refs that were dropped, with why — a signature
	// that did not verify, most often.
	Refused map[string]string
	// RefusedKind counts the same refusals by category rather than by
	// ref, because a metric cannot carry one series per branch and an
	// operator asking "is anything failing verification" wants the
	// number, not the messages. The categories are `signature` and
	// `materialise`.
	RefusedKind map[string]int
	// Withdrawn lists kept trees that were being served from an earlier
	// update and were dropped by this one because their commit no longer
	// verifies against the keyring, by ref, with why. Separate from
	// Refused: a refusal is a new tip that was never served, and this is
	// a tree that was, and the operator who withdrew a key needs to see
	// that the withdrawal took something away.
	Withdrawn map[string]string
}

// The reasons a ref is not served, as stable tokens.
const (
	RefusedSignature   = "signature"
	RefusedMaterialise = "materialise"
)

// refuse records one ref that is not served, with why.
func (r *RemoteState) refuse(ref, kind string, err error) {
	if r.Refused == nil {
		r.Refused = map[string]string{}
	}
	if r.RefusedKind == nil {
		r.RefusedKind = map[string]int{}
	}
	r.Refused[ref] = err.Error()
	r.RefusedKind[kind]++
}

// New checks the configuration and answers with a backend that has
// fetched nothing.
//
// Checked here so a bad remote stops the hub at startup, where the
// operator who wrote it is looking, rather than at the first update.
func New(opts Options) (*Backend, error) {
	if len(opts.Remotes) == 0 {
		return nil, fmt.Errorf("the git backend is enabled and no remotes are configured")
	}
	if opts.CacheDir == "" {
		return nil, fmt.Errorf("the git backend needs a cache directory")
	}
	for _, remote := range opts.Remotes {
		if err := checkURL(remote); err != nil {
			return nil, err
		}
	}
	if opts.VerifySignatures && opts.Keyring == "" {
		return nil, fmt.Errorf("gitfs_verify_signatures is on and gitfs_keyring is not set; " +
			"verification against the hub user's own keyring is not a decision anybody made")
	}
	if _, err := opts.refTypesChecked(); err != nil {
		return nil, err
	}
	return &Backend{opts: opts, roots: map[string][]string{}}, nil
}

func (o *Options) refTypesChecked() ([]string, error) {
	for _, t := range o.refTypes() {
		switch t {
		case "branch", "branches", "tag", "tags":
		default:
			return nil, fmt.Errorf("%q is not a ref type; use `branches` or `tags`", t)
		}
	}
	return o.refTypes(), nil
}

// Update fetches every remote and materialises what it serves.
//
// A remote that fails does not stop the others, and it does not clear
// what it was already serving: a network blip must not empty the file
// server. The failure is logged and carried in the state, and the tree
// that was there stays there — as long as, with verification on, it
// still verifies against the keyring as it is now. A tree whose signer
// has been withdrawn is dropped, by name, in the log and in the state.
func (b *Backend) Update(ctx context.Context) error {
	roots := map[string][]string{}
	var state []RemoteState
	var failures []string
	// reached are the remotes whose ref list was read, and so whose
	// cache the sweep may tidy. The sweep runs after the decision about
	// what is served, not inside the loop: see sweep.
	var reached []Remote
	// perRemote is what each remote served this time, by the key that
	// names its mirror. A remote that fails is not left out of what is
	// served: its last good roots are carried forward, below.
	perRemote := map[string][]servedRef{}

	for _, remote := range b.opts.Remotes {
		result := RemoteState{Remote: remote, Refused: map[string]string{}}
		mirror, err := b.opts.mirror(ctx, remote)
		if err != nil {
			result.Err = err
			state = append(state, result)
			failures = append(failures, remote.label())
			b.opts.log("warn", "a gitfs remote could not be updated",
				"remote", remote.label(), "error", err.Error())
			continue
		}
		refs, err := b.opts.refs(ctx, mirror, remote)
		if err != nil {
			result.Err = err
			state = append(state, result)
			failures = append(failures, remote.label())
			continue
		}

		for _, ref := range refs {
			if err := b.opts.verify(ctx, mirror, remote, ref); err != nil {
				// Not served. SPEC 13.3: a ref that fails verification
				// is not served, which is the difference between a
				// control and a log line.
				result.refuse(ref.Name, RefusedSignature, err)
				b.opts.log("warn", "a gitfs ref is not served because its signature did not verify",
					"remote", remote.label(), "ref", ref.Name, "error", err.Error())
				continue
			}
			target := filepath.Join(b.opts.CacheDir, remote.mirrorDir(), "trees", ref.Commit)
			if err := b.opts.materialise(ctx, mirror, target, remote, ref); err != nil {
				result.refuse(ref.Name, RefusedMaterialise, err)
				b.opts.log("warn", "a gitfs ref could not be materialised",
					"remote", remote.label(), "ref", ref.Name, "error", err.Error())
				continue
			}
			perRemote[remote.mirrorDir()] = append(perRemote[remote.mirrorDir()], servedRef{Ref: ref, Dir: target})
			result.Refs = append(result.Refs, ref)
		}
		if len(result.Refs) == 0 {
			// The remote was reachable and serves nothing: every ref
			// was refused, or it has none this configuration exposes.
			// Counted as a failure so the previous tree is kept rather
			// than the file server being emptied, provided it still
			// verifies: a push nobody trusted does not take the
			// estate's state away, but a key withdrawn from the
			// keyring takes away the trees it signed, which is what a
			// restart does (see reverify).
			failures = append(failures, remote.label())
			b.opts.log("warn", "a gitfs remote serves no environment",
				"remote", remote.label(), "refused", len(result.Refused))
		}
		state = append(state, result)
		reached = append(reached, remote)
	}

	// A remote that failed, or that served nothing, keeps what it was
	// serving. Only the case where every remote failed used to: with one
	// of two down, what was served was replaced by what the other one
	// returned, and the failed remote's environments disappeared from the
	// file server while their last good trees sat on disk, which the
	// comment above this function says does not happen. DIVERGENCE 5.550.
	b.mu.Lock()
	previous := b.byRemote
	b.mu.Unlock()
	for i, remote := range b.opts.Remotes {
		key := remote.mirrorDir()
		if len(perRemote[key]) != 0 {
			continue
		}
		kept, withdrawn := b.reverify(ctx, remote, previous[key])
		if len(kept) > 0 {
			perRemote[key] = kept
		}
		if len(withdrawn) > 0 {
			// state holds one entry per remote, in the order listed.
			state[i].Withdrawn = withdrawn
			if len(kept) == 0 {
				b.opts.log("warn", "a gitfs remote now serves nothing: no tree it was serving still verifies against the keyring",
					"remote", remote.label(), "withdrawn", len(withdrawn))
			}
		}
	}

	b.mu.Lock()
	b.byRemote = perRemote
	// In the order the remotes are listed, as before, so that a remote
	// listed first still shadows a later one for the same path.
	for _, remote := range b.opts.Remotes {
		for _, served := range perRemote[remote.mirrorDir()] {
			roots[served.Ref.Env] = append(roots[served.Ref.Env], served.Dir)
		}
	}
	// What is served is replaced every time, even when it is now
	// nothing. There used to be a second guard here, "a total failure
	// leaves the previous tree in place", from before the carry-forward
	// was per remote. Since 5.550 it is the carry-forward that keeps a
	// failed remote's tree, and the only thing the second guard could
	// still keep was a tree the carry-forward had just dropped because
	// its signer is no longer trusted: on a hub with one remote, a
	// withdrawn key emptied byRemote and the old roots stayed served
	// anyway (DIVERGENCE 5.584).
	b.roots = roots
	if len(roots) > 0 || len(failures) == 0 {
		b.updated = b.now()
	}
	b.state = state
	served := map[string]bool{}
	for _, dirs := range b.roots {
		for _, dir := range dirs {
			served[dir] = true
		}
	}
	b.mu.Unlock()

	for _, remote := range reached {
		b.sweep(remote, served)
	}

	if len(failures) == len(b.opts.Remotes) {
		return fmt.Errorf("no gitfs remote served anything: %s", strings.Join(failures, ", "))
	}
	if len(failures) > 0 {
		return fmt.Errorf("some gitfs remotes served nothing: %s", strings.Join(failures, ", "))
	}
	return nil
}

// reverify checks the trees a remote was serving against the keyring as
// it is now, and answers with the ones that still verify and, by ref,
// why each of the others was dropped.
//
// A remote whose update served nothing keeps its last good trees, which
// is right for a network blip and a refused push. It was also what
// happened when a signing key was withdrawn from the keyring: the new
// tip failed verification, the remote served nothing, and the tree the
// withdrawn key had signed was carried forward and served for as long as
// the hub ran. A hub restarted with the same keyring has nothing to carry
// and served nothing for that remote. Those are two paths that must agree,
// and a key is withdrawn most often because it was compromised, when the
// kept tree is the one most likely to be the attacker's (issue 623,
// DIVERGENCE 5.584).
//
// So a kept tree is verified again on every update that would carry it,
// against the same keyring and by the same `verify` that admitted it.
// Every update, rather than only when the keyring is seen to change,
// because the keyring is a GnuPG home that gpg reads afresh on each call:
// the hub has never noticed a change to it, it just asks gpg, and a
// second path that tried to notice (a digest of pubring.kbx, say) would be
// a second thing to disagree — a revocation imported into the keyring, a
// trustdb edit or an expiry would each change what gpg says without
// necessarily changing what the digest covered. With the same question
// asked of the same gpg, a running hub and a restarted one answer alike
// by construction. The cost is one `git verify-commit` per kept ref on an
// update where its remote served nothing, which is the uncommon case.
//
// The mirror the tree came from is used whether or not this update
// reached the remote: verification reads objects already in it, and a
// remote that is unreachable is exactly when the kept tree is served.
// A mirror that is gone (the cache was deleted) cannot verify anything,
// and its trees are gone with it.
func (b *Backend) reverify(ctx context.Context, remote Remote, previous []servedRef) ([]servedRef, map[string]string) {
	if !b.opts.VerifySignatures || len(previous) == 0 {
		return previous, nil
	}
	mirror := filepath.Join(b.opts.CacheDir, remote.mirrorDir())
	var kept []servedRef
	var withdrawn map[string]string
	for _, served := range previous {
		if err := b.opts.verify(ctx, mirror, remote, served.Ref); err != nil {
			if withdrawn == nil {
				withdrawn = map[string]string{}
			}
			withdrawn[served.Ref.Name] = err.Error()
			b.opts.log("warn", "a gitfs tree is no longer served because its signature no longer verifies",
				"remote", remote.label(), "ref", served.Ref.Name, "environment", served.Ref.Env,
				"commit", served.Ref.Commit, "error", err.Error())
			continue
		}
		kept = append(kept, served)
	}
	return kept, withdrawn
}

// Refusals totals the last update's refused refs by category, across
// every remote, for a caller keeping metrics.
func (b *Backend) Refusals() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]int{}
	for _, remote := range b.state {
		for kind, n := range remote.RefusedKind {
			out[kind] += n
		}
	}
	return out
}

// sweep removes materialised trees that nothing is serving, so the
// cache does not grow with every push.
//
// What is live is what the backend is actually serving after this
// update — the directories in b.roots — and not what this update's refs
// resolved to. The two differ exactly when an update keeps the last
// good tree: every ref refused (a signature, a symlink, a blob too
// large to unpack) leaves b.roots pointing at the previous commit's
// directory while the refs that resolved are none. Keyed on the refs,
// the sweep deleted that directory and left the file server listing a
// root with no files in it, which is the empty estate the kept tree
// was there to prevent (DIVERGENCE 5.293). Keyed on what is served,
// the sweep and the decision about what to serve cannot disagree,
// because the second is the input to the first.
func (b *Backend) sweep(remote Remote, served map[string]bool) {
	dir := filepath.Join(b.opts.CacheDir, remote.mirrorDir(), "trees")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if !e.IsDir() || served[path] {
			continue
		}
		// Anything else goes, a `.building` staging directory left by
		// an interrupted update included: removing those is part of
		// the point of the sweep.
		_ = os.RemoveAll(path)
	}
}

// Roots is what the file server should serve, by environment.
func (b *Backend) Roots() map[string][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string][]string, len(b.roots))
	for env, dirs := range b.roots {
		out[env] = append([]string(nil), dirs...)
	}
	return out
}

// Envs is every environment this backend serves, in order.
func (b *Backend) Envs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.roots))
	for env := range b.roots {
		out = append(out, env)
	}
	sort.Strings(out)
	return out
}

// State is what each remote resolved to, for `fileserver.update` and
// for a report an operator reads.
func (b *Backend) State() []RemoteState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]RemoteState(nil), b.state...)
}

// Describe renders the state for an operator.
func (b *Backend) Describe() []map[string]any {
	var out []map[string]any
	for _, state := range b.State() {
		entry := map[string]any{"remote": state.Remote.label()}
		if state.Err != nil {
			entry["error"] = state.Err.Error()
		}
		envs := map[string]any{}
		for _, ref := range state.Refs {
			envs[ref.Env] = map[string]any{
				"ref": ref.Name, "commit": ref.Commit, "tag": ref.Tag,
			}
		}
		entry["environments"] = envs
		if len(state.Refused) > 0 {
			refused := map[string]any{}
			for ref, why := range state.Refused {
				refused[ref] = why
			}
			entry["refused"] = refused
		}
		if len(state.Withdrawn) > 0 {
			withdrawn := map[string]any{}
			for ref, why := range state.Withdrawn {
				withdrawn[ref] = why
			}
			entry["withdrawn"] = withdrawn
		}
		out = append(out, entry)
	}
	return out
}

func (b *Backend) now() time.Time {
	if b.opts.Now != nil {
		return b.opts.Now()
	}
	return time.Now()
}
