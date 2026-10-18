package fileserver

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
)

// Fetcher adapts a roots file server to the interface a module uses to
// resolve a halite:// or salt:// source.
//
// On a node talking to a hub the fetch is a transfer that is verified
// against the manifest hash and written atomically; against local roots it
// resolves to the file in place, because there is nothing to transfer.
type Fetcher struct {
	// Embedded so that one value is both the state compiler's Loader
	// and a module's FileFetcher, which is what lets a run compile
	// against local roots or against a hub without knowing which.
	*Roots
	// HashType is the digest the file server reports.
	HashType string
}

// NewFetcher wraps a roots file server.
func NewFetcher(r *Roots) *Fetcher { return &Fetcher{Roots: r, HashType: "sha256"} }

// Fetch implements exec.FileFetcher.
func (f *Fetcher) Fetch(env, uri string) (string, error) {
	target := EnvFromURI(uri, env)
	path, err := f.Resolve(target, StripScheme(uri))
	if err != nil {
		return "", fmt.Errorf("%s is not served from the %q environment: %w", uri, target, err)
	}
	return path, nil
}

// Hash implements exec.FileFetcher.
func (f *Fetcher) Hash(env, uri string) (string, string, error) {
	path, err := f.Fetch(env, uri)
	if err != nil {
		return "", "", err
	}
	algorithm := f.HashType
	if algorithm == "" {
		algorithm = "sha256"
	}
	digest, err := hashOf(path, algorithm)
	if err != nil {
		return "", "", err
	}
	return algorithm, digest, nil
}

// Exists implements exec.FileFetcher.
func (f *Fetcher) Exists(env, uri string) bool {
	_, err := f.Fetch(env, uri)
	return err == nil
}

// compile-time check that the adapter satisfies what a module expects.
var _ exec.FileFetcher = (*Fetcher)(nil)

// ListUnder implements exec.FileLister over a local tree.
func (f *Fetcher) ListUnder(env, prefix string) ([]string, error) {
	target := EnvFromURI(prefix, env)
	m, err := f.Roots.Manifest(target, StripScheme(prefix), f.HashType)
	if err != nil {
		return nil, err
	}
	return relativeTo(StripScheme(prefix), m)
}

// relativeTo trims the prefix from a manifest's entries, so a caller
// gets the paths as they will be laid out under a destination rather
// than as they sit in the tree.
func relativeTo(prefix string, m *Manifest) ([]string, error) {
	prefix = strings.Trim(prefix, "/")
	out := make([]string, 0, len(m.Files))
	for _, e := range m.Files {
		p := strings.Trim(e.Path, "/")
		// The prefix is a directory, matched on a path boundary. It was
		// trimmed as a string, so under "web" an entry "webby/x" came back
		// as "by/x": a file of another directory, copied in.
		var rel string
		switch {
		case prefix == "":
			rel = p
		case p == prefix:
			continue
		case strings.HasPrefix(p, prefix+"/"):
			rel = p[len(prefix)+1:]
		default:
			continue
		}
		if rel == "" {
			continue
		}
		// A path that leaves the destination is refused, not laid out.
		// Only a hub that is lying sends one, and SPEC 25.6 does not trust
		// the hub: "x/../../../etc/cron.d/evil" went to file.recurse as it
		// stood and was written there (issue 321, section 5).
		if !path.IsAbs(rel) && path.Clean(rel) == rel && !strings.HasPrefix(rel, "../") && rel != ".." {
			out = append(out, rel)
			continue
		}
		return nil, fmt.Errorf("the listing of %q holds %q, which is not a path inside it", prefix, e.Path)
	}
	sort.Strings(out)
	return out, nil
}
