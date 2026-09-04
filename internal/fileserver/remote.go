package fileserver

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/edlitmus/halite/internal/atomicfile"
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

	mu sync.Mutex
	// digests remembers what each cached path was verified as, so a
	// second request in one run does not re-read the file to hash it.
	digests map[string]string
}

// Fetch is the part of the transport this package needs. Defined here,
// on the consumer, so that internal/fileserver does not depend on
// internal/transport.
type Fetch interface {
	FileManifest(ctx context.Context, env, prefix string) ([]byte, error)
	FetchFile(ctx context.Context, env, path, etag string) (body []byte, digest string, notModified bool, err error)
}

// NewRemote builds a hub-backed file server.
func NewRemote(client Fetch, dir string, envs []string) *Remote {
	return &Remote{Client: client, Dir: dir, Environments: envs, digests: map[string]string{}}
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
func (r *Remote) Source(env, sls string) ([]byte, string, error) {
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
	r.mu.Lock()
	digest, ok := r.digests[path]
	r.mu.Unlock()
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

	body, published, notModified, err := r.Client.FetchFile(context.Background(), env, rel, etag)
	if err != nil {
		return "", err
	}
	if notModified {
		r.remember(path, strings.TrimPrefix(etag, r.hashType()+":"))
		return path, nil
	}

	// SPEC 13.5: the transfer is integrity-checked against the
	// published digest after write and before the file is moved into
	// place, so a truncated or tampered transfer never becomes the
	// cached copy.
	if published != "" {
		algorithm, want, found := strings.Cut(published, ":")
		if found {
			got, err := digestOf(body, algorithm)
			if err != nil {
				return "", err
			}
			if got != want {
				return "", fmt.Errorf("%s/%s arrived with digest %s and the hub published %s", env, rel, got, want)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("caching %s: %w", rel, err)
	}
	if err := writeAtomic(path, body, 0o600); err != nil {
		return "", err
	}
	if published != "" {
		_, digest, _ := strings.Cut(published, ":")
		r.remember(path, digest)
	}
	return path, nil
}

func (r *Remote) remember(path, digest string) {
	if digest == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.digests[path] = digest
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
	raw, err := r.Client.FileManifest(context.Background(), env, prefix)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("the hub's file listing is not readable: %w", err)
	}
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
	var h hash.Hash
	switch algorithm {
	case "sha256", "":
		h = sha256.New()
	case "sha384":
		h = sha512.New384()
	case "sha512":
		h = sha512.New()
	default:
		return "", fmt.Errorf("%q is not a hash this build verifies", algorithm)
	}
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeAtomic writes through a temporary file in the same directory.
//
// The idiom is one package rather than six copies of it, because all six
// were wrong on Windows in the same way: see internal/atomicfile.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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
	raw, err := r.Client.FileManifest(context.Background(), env, prefix)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("the hub's listing of %s is not readable: %w", prefix, err)
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
	return relativeTo(StripScheme(prefix), m), nil
}
