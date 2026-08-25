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
// that was there stays there.
func (b *Backend) Update(ctx context.Context) error {
	roots := map[string][]string{}
	var state []RemoteState
	var failures []string

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
				result.Refused[ref.Name] = err.Error()
				b.opts.log("warn", "a gitfs ref is not served because its signature did not verify",
					"remote", remote.label(), "ref", ref.Name, "error", err.Error())
				continue
			}
			target := filepath.Join(b.opts.CacheDir, remote.mirrorDir(), "trees", ref.Commit)
			if err := b.opts.materialise(ctx, mirror, target, remote, ref); err != nil {
				result.Refused[ref.Name] = err.Error()
				b.opts.log("warn", "a gitfs ref could not be materialised",
					"remote", remote.label(), "ref", ref.Name, "error", err.Error())
				continue
			}
			roots[ref.Env] = append(roots[ref.Env], target)
			result.Refs = append(result.Refs, ref)
		}
		if len(result.Refs) == 0 {
			// The remote was reachable and serves nothing: every ref
			// was refused, or it has none this configuration exposes.
			// Counted as a failure so the previous tree is kept rather
			// than the file server being emptied — an operator who
			// withdraws trust from a signing key should get the last
			// tree that verified, not an estate with no state at all.
			failures = append(failures, remote.label())
			b.opts.log("warn", "a gitfs remote serves no environment",
				"remote", remote.label(), "refused", len(result.Refused))
		}
		state = append(state, result)
		b.sweep(remote, result.Refs)
	}

	b.mu.Lock()
	if len(roots) > 0 || len(failures) == 0 {
		// Only replace what is served when something was served, or
		// when nothing failed. A total failure leaves the previous
		// tree in place rather than emptying the file server.
		b.roots = roots
		b.updated = b.now()
	}
	b.state = state
	b.mu.Unlock()

	if len(failures) == len(b.opts.Remotes) {
		return fmt.Errorf("no gitfs remote served anything: %s", strings.Join(failures, ", "))
	}
	if len(failures) > 0 {
		return fmt.Errorf("some gitfs remotes served nothing: %s", strings.Join(failures, ", "))
	}
	return nil
}

// sweep removes materialised trees for commits no ref points at any
// more, so the cache does not grow with every push.
func (b *Backend) sweep(remote Remote, refs []Ref) {
	dir := filepath.Join(b.opts.CacheDir, remote.mirrorDir(), "trees")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, ref := range refs {
		live[ref.Commit] = true
	}
	for _, e := range entries {
		if !e.IsDir() || live[e.Name()] {
			continue
		}
		if strings.HasSuffix(e.Name(), ".building") {
			// A staging directory left by an update that was
			// interrupted. Removing it is the point of the sweep.
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
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

// Updated is when the last successful update finished.
func (b *Backend) Updated() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.updated
}
