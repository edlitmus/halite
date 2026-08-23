package fileserver

import (
	"fmt"

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
