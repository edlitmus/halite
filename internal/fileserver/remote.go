package fileserver

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/template"
)

// Remote is the file server as a node sees it: the hub's tree, cached
// on disk.
//
// It satisfies the same two interfaces the local roots do -- the state
// compiler's Loader and a module's FileFetcher -- so a state that says
// `source: salt://web/nginx.conf` does not know or care which one it
// got. That is the point: the tree is the tree.
type Remote struct {
	// Client fetches from the hub.
	Client Fetch
	// Dir is where fetched files are kept, under the node's cache.
	Dir string
	// Environments is what the hub serves, learnt at first use.
	Environments []string
	// HashType is the digest to verify against.
	HashType string
	// Unreachable says whether a fetch failed because no hub answered, as
	// opposed to a hub answering no. Only then is the cached copy served.
	// Nil means never, so a Remote nobody configured behaves as it always
	// did. On the consumer for the reason Fetch is: this package does not
	// depend on internal/transport.
	Unreachable func(error) bool
	// Warn is told once per outage that the tree is being served from the
	// cache.
	Warn func(msg string, kv ...any)

	// ctx is what a transfer's request is made under.
	//
	// It carries two things a background context cannot: the cancellation
	// of the job that asked for the file, so that `jobs kill` stops a
	// fetch instead of leaving it running, and the trace context of the
	// state that asked, so the hub's side of the transfer joins the
	// node's trace. Nil means background, which is what a Remote built
	// outside a run gets.
	ctx context.Context

	// cache is shared between the copies WithContext makes, by pointer,
	// so that a per-run copy does not re-hash a file the last one
	// verified -- and so that copying a Remote does not copy a mutex.
	cache *digestCache
}

// digestCache remembers what each cached path was verified as, so a
// second request in one run does not re-read the file to hash it.
type digestCache struct {
	mu sync.Mutex
	by map[string]string
	// retryAt is when to ask the hub again after it did not answer.
	//
	// Without it a hub that drops packets rather than refusing them costs
	// a handshake timeout per file, and a highstate over a few hundred
	// files spends the whole of its schedule interval waiting to be told
	// the same thing.
	retryAt time.Time
	warned  bool
}

// offlineRetry is how long a hub that did not answer is left alone.
//
// Chosen, not measured: long enough that one run makes a handful of attempts
// rather than one per file, short enough that a hub coming back is noticed
// within a run.
const offlineRetry = 30 * time.Second

func (r *Remote) holdingOff() bool {
	r.cache.mu.Lock()
	defer r.cache.mu.Unlock()
	return time.Now().Before(r.cache.retryAt)
}

func (r *Remote) noteUnreachable(err error) {
	r.cache.mu.Lock()
	r.cache.retryAt = time.Now().Add(offlineRetry)
	warn := !r.cache.warned
	r.cache.warned = true
	r.cache.mu.Unlock()
	if warn && r.Warn != nil {
		r.Warn("the hub cannot be reached; serving the tree from this node's cache",
			"cache", r.Dir, "error", err.Error())
	}
}

func (r *Remote) noteAnswered() {
	r.cache.mu.Lock()
	r.cache.retryAt = time.Time{}
	r.cache.warned = false
	r.cache.mu.Unlock()
}

func (r *Remote) unreachable(err error) bool {
	return err != nil && r.Unreachable != nil && r.Unreachable(err)
}

// Fetch is the part of the transport this package needs. Defined here,
// on the consumer, so that internal/fileserver does not depend on
// internal/transport.
type Fetch interface {
	FileManifest(ctx context.Context, env, prefix string) ([]byte, error)
	FetchFile(ctx context.Context, env, path, etag string) (body []byte, digest string, notModified bool, err error)
}

// WithContext returns a Remote whose transfers are made under ctx.
//
// A copy rather than a setter, because one node serves many runs and a
// field set by whichever run started last would attach a job's
// cancellation to another job's fetch. The digest cache is shared by
// pointer: it is about what is on disk, which is the same fact for every
// run, and re-verifying a file per run would undo the reason it exists.
func (r *Remote) WithContext(ctx context.Context) *Remote {
	if r == nil {
		return nil
	}
	copied := *r
	copied.ctx = ctx
	return &copied
}

func (r *Remote) context() context.Context {
	if r.ctx != nil {
		return r.ctx
	}
	return context.Background()
}

// NewRemote builds a hub-backed file server.
func NewRemote(client Fetch, dir string, envs []string) *Remote {
	return &Remote{Client: client, Dir: dir, Environments: envs, cache: &digestCache{by: map[string]string{}}}
}

func (r *Remote) hashType() string {
	if r.HashType == "" {
		return "sha256"
	}
	return r.HashType
}

// Envs implements state.Loader.
func (r *Remote) Envs() []string {
	if len(r.Environments) == 0 {
		return []string{"base"}
	}
	out := append([]string(nil), r.Environments...)
	sort.Strings(out)
	for i, e := range out {
		if e == "base" && i != 0 {
			out = append([]string{"base"}, append(out[:i:i], out[i+1:]...)...)
			break
		}
	}
	return out
}

// Source implements state.Loader: a dotted SLS name to its bytes.
//
// A non-canonical name is refused before it is fetched, as Roots.Source
// does. DIVERGENCE 5.294.
func (r *Remote) Source(env, sls string) ([]byte, string, error) {
	if _, err := state.CanonicalSLS(sls); err != nil {
		return nil, "", err
	}
	var tried []string
	for _, candidate := range slsCandidates(sls) {
		body, err := r.Read(env, candidate)
		if err == nil {
			return body, "halite://" + env + "/" + candidate, nil
		}
		tried = append(tried, candidate)
	}
	return nil, "", fmt.Errorf("%w: %s in environment %q (tried %s)",
		state.ErrNotFound, sls, env, strings.Join(tried, ", "))
}

// Templates implements state.Loader.
func (r *Remote) Templates(env string) template.Loader {
	return remoteTemplates{remote: r, env: env}
}

type remoteTemplates struct {
	remote *Remote
	env    string
}

func (t remoteTemplates) Load(name string) (string, string, error) {
	for _, candidate := range templateCandidates(name) {
		body, err := t.remote.Read(t.env, candidate)
		if err == nil {
			return string(body), "halite://" + t.env + "/" + candidate, nil
		}
	}
	return "", "", fmt.Errorf("template %q is not served from the %q environment", name, t.env)
}

// Read fetches a path's contents, through the cache.
func (r *Remote) Read(env, rel string) ([]byte, error) {
	path, err := r.cacheFile(env, rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Fetch implements exec.FileFetcher: it returns a local path to the
// file, which is the cached copy.
func (r *Remote) Fetch(env, uri string) (string, error) {
	target := EnvFromURI(uri, env)
	return r.cacheFile(target, StripScheme(uri))
}

// Hash implements exec.FileFetcher.
func (r *Remote) Hash(env, uri string) (string, string, error) {
	path, err := r.Fetch(env, uri)
	if err != nil {
		return "", "", err
	}
	r.cache.mu.Lock()
	digest, ok := r.cache.by[path]
	r.cache.mu.Unlock()
	if ok {
		return r.hashType(), digest, nil
	}
	digest, err = hashOf(path, r.hashType())
	if err != nil {
		return "", "", err
	}
	return r.hashType(), digest, nil
}

// Exists implements exec.FileFetcher.
func (r *Remote) Exists(env, uri string) bool {
	_, err := r.Fetch(env, uri)
	return err == nil
}

// cacheFile makes sure a file is present and current, and returns where
// it is.
//
// The digest the node already has goes up as the entity tag, so an
// unchanged file costs a 304 and no body: a tree redeployed from git
// has new timestamps and identical contents, and re-sending an estate's
// whole tree on every deploy is what makes Salt's file server the
// bottleneck it is.
func (r *Remote) cacheFile(env, rel string) (string, error) {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "/")
	if rel == "" {
		return "", fmt.Errorf("no path was asked for")
	}
	path, err := r.cachePath(env, rel)
	if err != nil {
		return "", err
	}

	etag := ""
	if existing, err := hashOf(path, r.hashType()); err == nil {
		etag = r.hashType() + ":" + existing
	}

	if r.Unreachable != nil && r.holdingOff() {
		if etag != "" {
			return path, nil
		}
		return "", fmt.Errorf("the hub cannot be reached and this node has no cached copy of %s/%s", env, rel)
	}
	body, published, notModified, err := r.Client.FetchFile(r.context(), env, rel, etag)
	if err != nil {
		// A copy here was verified against the hub's digest before it was
		// moved into place, and has been readable only by this account
		// since. It may be older than the hub's: that is what a last known
		// good tree is. Only "nobody answered" gets here. A hub that says
		// a file is gone or refused has answered, and the error stands.
		if r.unreachable(err) {
			r.noteUnreachable(err)
			if etag != "" {
				return path, nil
			}
		}
		return "", err
	}
	r.noteAnswered()
	if notModified {
		r.remember(path, strings.TrimPrefix(etag, r.hashType()+":"))
		return path, nil
	}

	// SPEC 13.5: the transfer is integrity-checked against the
	// published digest after write and before the file is moved into
	// place, so a truncated or tampered transfer never becomes the
	// cached copy.
	//
	// A body without a digest is refused, and so is a digest without an
	// algorithm. Both were taken as "nothing to check", and the hub sent
	// no digest whenever it could not hash the file -- for every file,
	// under a `hash_type` of sha3-256 (DIVERGENCE 5.469). A check that a
	// missing header turns off is not a check. Every hub this node can
	// talk to sends one: the header is as old as the file endpoint.
	if published == "" {
		return "", fmt.Errorf("the hub published no digest for %s/%s, so it cannot be checked and was not cached", env, rel)
	}
	algorithm, want, found := strings.Cut(published, ":")
	if !found || want == "" {
		return "", fmt.Errorf("the hub's digest for %s/%s, %q, is not a digest: it names no algorithm", env, rel, published)
	}
	got, err := digestOf(body, algorithm)
	if err != nil {
		return "", fmt.Errorf("%s/%s: %w", env, rel, err)
	}
	if got != want {
		return "", fmt.Errorf("%s/%s arrived with digest %s and the hub published %s", env, rel, got, want)
	}
	if err := fileperm.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("caching %s: %w", rel, err)
	}
	if err := writeAtomic(path, body, 0o600); err != nil {
		return "", err
	}
	r.remember(path, want)
	return path, nil
}

func (r *Remote) remember(path, digest string) {
	if digest == "" {
		return
	}
	r.cache.mu.Lock()
	defer r.cache.mu.Unlock()
	r.cache.by[path] = digest
}

// cachePath is where a file lands, refusing anything that would put it
// outside the cache. The hub is authenticated, and a hub that has been
// compromised should still not be able to write to the node's /etc.
func (r *Remote) cachePath(env, rel string) (string, error) {
	if r.Dir == "" {
		return "", fmt.Errorf("this node has no file cache directory")
	}
	if strings.Contains(env, "/") || strings.Contains(env, "..") || env == "" {
		return "", fmt.Errorf("%q is not an environment", env)
	}
	root := filepath.Join(r.Dir, "files", env)
	clean := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if !within(root, clean) {
		return "", fmt.Errorf("%w: %s would be cached outside %s", ErrOutsideRoot, rel, root)
	}
	return clean, nil
}

// ManifestFor reads the hub's listing for a subtree.
func (r *Remote) ManifestFor(env, prefix string) (*Manifest, error) {
	if r.Unreachable != nil && r.holdingOff() {
		return r.savedManifest(env, prefix, errors.New("the hub cannot be reached"))
	}
	raw, err := r.Client.FileManifest(r.context(), env, prefix)
	if err != nil {
		if r.unreachable(err) {
			r.noteUnreachable(err)
			return r.savedManifest(env, prefix, err)
		}
		return nil, err
	}
	r.noteAnswered()
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("the hub's file listing is not readable: %w", err)
	}
	// Only a whole listing is kept: a prefix's would overwrite it with a
	// part, and a truncated one would lay out part of a directory later.
	if prefix == "" && !m.Truncated && r.Dir != "" {
		if path, err := r.manifestPath(env); err == nil {
			if err := writeAtomic(path, raw, 0o600); err != nil && r.Warn != nil {
				r.Warn("could not keep the hub's file listing", "error", err.Error())
			}
		}
	}
	return &m, nil
}

// manifestPath is outside files/, so no path in the tree can collide with it.
func (r *Remote) manifestPath(env string) (string, error) {
	if strings.Contains(env, "/") || strings.Contains(env, "..") || env == "" {
		return "", fmt.Errorf("%q is not an environment", env)
	}
	return filepath.Join(r.Dir, "manifests", env+".json"), nil
}

// HasCachedTree reports whether the hub's tree for an environment was ever
// listed here. It is what lets a node restarted during an outage go on
// using that tree rather than its own roots.
func (r *Remote) HasCachedTree(env string) bool {
	path, err := r.manifestPath(env)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// savedManifest answers a listing from the last whole one, cut to the prefix
// on a path boundary. Without a saved listing the failure that brought it
// here is what the caller gets.
func (r *Remote) savedManifest(env, prefix string, cause error) (*Manifest, error) {
	path, err := r.manifestPath(env)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, cause
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("the saved file listing %s is not readable: %w", path, err)
	}
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return &m, nil
	}
	kept := make([]Entry, 0, len(m.Files))
	for _, e := range m.Files {
		p := strings.Trim(e.Path, "/")
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			kept = append(kept, e)
		}
	}
	m.Files, m.Prefix = kept, prefix
	return &m, nil
}

// List implements the part of the local Roots interface a caller uses
// to enumerate an environment.
func (r *Remote) List(env string) ([]string, error) {
	m, err := r.ManifestFor(env, "")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		out = append(out, f.Path)
	}
	return out, nil
}

func digestOf(data []byte, algorithm string) (string, error) {
	h, err := newHash(algorithm)
	if err != nil {
		return "", err
	}
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeAtomic writes through a temporary file in the same directory.
//
// The idiom is one package rather than six copies of it, because all six
// were wrong on Windows in the same way: see internal/atomicfile.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := fileperm.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return atomicfile.Write(path, data, mode)
}

// ForEnv returns a view of the same hub tree, which is the same value:
// an environment is a parameter of every call rather than state on the
// fetcher.
//
// It exists so that a caller switching environments has one thing to
// say, and so that switching cannot accidentally discard the cache.
func (r *Remote) ForEnv(env string) *Remote {
	if len(r.Environments) == 0 {
		r.Environments = []string{env}
		return r
	}
	for _, e := range r.Environments {
		if e == env {
			return r
		}
	}
	r.Environments = append(r.Environments, env)
	return r
}

// ListPrefix returns the paths under a prefix, with the digests the hub
// published.
//
// For extension synchronization, which needs to know what is in `_ext/`
// before it can fetch it — every other consumer here asks for a path it
// already knows the name of.
func (r *Remote) ListPrefix(env, prefix string) ([]Entry, error) {
	manifest, err := r.ManifestFor(env, prefix)
	if err != nil {
		return nil, err
	}
	if manifest.Truncated {
		// A truncated listing would make synchronization fetch part of
		// a bundle and verify the part, which fails in a way that
		// blames the publisher.
		return nil, fmt.Errorf("the hub's listing of %s was truncated at %d entries",
			prefix, len(manifest.Files))
	}
	return manifest.Files, nil
}

// ListUnder implements exec.FileLister against a hub.
//
// The same trimming as the local fetcher, so a state laying files out
// under a destination sees the same relative paths whether the tree came
// from this node's roots or from the hub. A recursive copy that placed
// files differently depending on where the tree lived would be worse
// than one that did not work.
func (r *Remote) ListUnder(env, prefix string) ([]string, error) {
	target := EnvFromURI(prefix, env)
	m, err := r.ManifestFor(target, StripScheme(prefix))
	if err != nil {
		return nil, err
	}
	return relativeTo(StripScheme(prefix), m)
}
