package fileserver

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Entry is one file in a manifest: its path relative to the
// environment's root, its digest, and its size.
type Entry struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// Manifest is a subtree's contents with hashes and sizes.
//
// SPEC 13.5: a node fetches this once and then asks only for the files
// whose digest differs, rather than making one round trip per file.
// `file.recurse` over a large tree is the case that makes the
// difference.
type Manifest struct {
	Env       string  `json:"env"`
	Prefix    string  `json:"prefix,omitempty"`
	HashType  string  `json:"hash_type"`
	Files     []Entry `json:"files"`
	Truncated bool    `json:"truncated,omitempty"`
}

// MaxManifestEntries bounds a listing, so that a request for `/` over a
// tree with a million files does not build the answer in memory first.
const MaxManifestEntries = 50000

// Manifest builds the listing for an environment, optionally limited to
// a prefix.
//
// The prefix goes through the same containment check as a fetch: SPEC
// 13.5 requires that *every* request be checked, and Salt's
// CVE-2020-11652 was a traversal in this code path rather than in the
// fetch one.
func (r *Roots) Manifest(env, prefix, hashType string) (*Manifest, error) {
	if hashType == "" {
		hashType = "sha256"
	}
	prefix = strings.TrimPrefix(prefix, "/")
	if prefix != "" {
		// Resolving it proves it is inside a root before anything is
		// listed. A prefix that does not exist is not an error -- an
		// empty subtree is a legitimate answer -- but one that escapes
		// is.
		if _, err := r.Resolve(env, prefix); errors.Is(err, ErrOutsideRoot) {
			return nil, err
		}
	}

	paths, err := r.List(env)
	if err != nil {
		return nil, err
	}
	out := &Manifest{Env: env, Prefix: prefix, HashType: hashType}
	for _, rel := range paths {
		if prefix != "" && !strings.HasPrefix(rel, prefix) {
			continue
		}
		if len(out.Files) >= MaxManifestEntries {
			out.Truncated = true
			break
		}
		path, err := r.Resolve(env, rel)
		if err != nil {
			// Listed and not resolvable means it was removed between
			// the walk and here, or it is a symlink out of the root
			// that the resolver refuses. Neither belongs in a manifest.
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		digest, err := hashOf(path, hashType)
		if err != nil {
			return nil, fmt.Errorf("hashing %s: %w", rel, err)
		}
		out.Files = append(out.Files, Entry{Path: rel, Hash: digest, Size: info.Size()})
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	return out, nil
}

// HashOf is one file's digest, for a conditional request and for the
// integrity check a node makes after writing.
//
// Not `Hash`: Fetcher embeds Roots and has its own Hash with a
// different signature, and two methods of the same name one shadowing
// the other is a trap for whoever reads it next.
func (r *Roots) HashOf(env, rel, hashType string) (string, string, error) {
	if hashType == "" {
		hashType = "sha256"
	}
	path, err := r.Resolve(env, rel)
	if err != nil {
		return "", "", err
	}
	digest, err := hashOf(path, hashType)
	if err != nil {
		return "", "", err
	}
	return hashType, digest, nil
}
