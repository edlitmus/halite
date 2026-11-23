package fileserver

import (
	"errors"
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/treesign"
)

// Signed state content over this node's own roots: `state_signature_local`,
// on by default (a maintainer decision, issue 764).
//
// Without it, the requirement would have a way round it: a node whose hub
// does not serve files at startup applies its own tree, and so does
// `--local`. Root on the node can turn the setting off, so this is about
// one rule everywhere rather than security against the node's own root.
//
// The methods below shadow the embedded Roots' when the guard is active,
// and fall through to them when it is not, so a Fetcher nobody gave a
// guard is exactly what it was.

// ForRun returns a Fetcher that reads each environment at one signed
// revision for as long as it is used.
func (f *Fetcher) ForRun() *Fetcher {
	if f == nil {
		return nil
	}
	copied := *f
	copied.run = NewRun()
	return &copied
}

func (f *Fetcher) signedRevision(env string) (*Revision, error) {
	return f.Guard.revision(f.run, env, f.localSigned)
}

func (f *Fetcher) localSigned(env string) ([]byte, []byte, bool, error) {
	sig, _, err := f.Roots.Read(env, treesign.SignaturePath)
	if err != nil {
		return nil, nil, false, err
	}
	manifest, _, err := f.Roots.Read(env, treesign.ManifestPath)
	if err != nil {
		return nil, nil, false, err
	}
	return manifest, sig, false, nil
}

// verified is a path's verified copy, or "" with a nil error where warn
// mode says to read the tree as it is.
//
// quiet is a candidate search's miss, which is not counted as a refusal:
// see Remote.readCandidate.
func (f *Fetcher) verified(env, rel string, quiet bool) (string, error) {
	rel = cleanRel(rel)
	rev, err := f.signedRevision(env)
	if err != nil || rev == nil {
		return "", err
	}
	entry, ok := rev.Manifest.Lookup(rel)
	if !ok {
		refusal := notInRevision(env, rel, rev)
		if f.Guard.Mode == SignatureWarn {
			if _, err := f.Roots.Resolve(env, rel); err == nil {
				f.Guard.note(refusal)
			}
			return "", nil
		}
		if !quiet {
			f.Guard.note(refusal)
		}
		return "", refusal
	}
	if object, have := f.Guard.haveObject(f.digests(), rev, entry); have {
		// Still read and compared below: a local file can change under a
		// node without any request saying so, and the object being here
		// says only that it was once right.
		body, _, err := f.Roots.Read(env, rel)
		if err == nil {
			if got, err := digestOf(body, rev.Manifest.HashType); err == nil && got == entry.Hash {
				return object, nil
			}
		}
	}
	body, _, err := f.Roots.Read(env, rel)
	if err != nil {
		return "", err
	}
	kept, err := f.Guard.keep(f.digests(), env, rev, entry, body, "")
	if err != nil {
		var refusal *treesign.Refusal
		if errors.As(err, &refusal) {
			return "", f.Guard.refuse(refusal)
		}
		return "", err
	}
	return kept, nil
}

// digests is the verified-object cache. A Fetcher built as a literal
// rather than by NewFetcher has none, and gets one per call, which costs a
// hash and is never wrong.
func (f *Fetcher) digests() *digestCache {
	if f.cache == nil {
		return &digestCache{by: map[string]string{}}
	}
	return f.cache
}

// readSigned reads a path, verified where the guard says so. ok is false
// where the caller should read the tree as it is.
func (f *Fetcher) readSigned(env, rel string) ([]byte, string, bool, error) {
	object, err := f.verified(env, rel, true)
	if err != nil {
		return nil, "", true, err
	}
	if object == "" {
		return nil, "", false, nil
	}
	body, err := readFile(object)
	return body, object, true, err
}

// Source implements state.Loader.
func (f *Fetcher) Source(env, sls string) ([]byte, string, error) {
	if !f.Guard.Active() {
		return f.Roots.Source(env, sls)
	}
	if _, err := state.CanonicalSLS(sls); err != nil {
		return nil, "", err
	}
	var tried []string
	for _, candidate := range slsCandidates(sls) {
		body, where, ok, err := f.readSigned(env, candidate)
		if !ok {
			return f.Roots.Source(env, sls)
		}
		if err == nil {
			return body, where, nil
		}
		if treesign.ReasonOf(err) != treesign.ReasonNotInManifest {
			return nil, "", err
		}
		tried = append(tried, candidate)
	}
	return nil, "", fmt.Errorf("%w: %s in environment %q (tried %s, in its signed revision)",
		state.ErrNotFound, sls, env, strings.Join(tried, ", "))
}

// Templates implements state.Loader.
func (f *Fetcher) Templates(env string) template.Loader {
	if !f.Guard.Active() {
		return f.Roots.Templates(env)
	}
	return signedLocalTemplates{f: f, env: env}
}

type signedLocalTemplates struct {
	f   *Fetcher
	env string
}

func (t signedLocalTemplates) Load(name string) (string, string, error) {
	for _, candidate := range templateCandidates(name) {
		body, where, ok, err := t.f.readSigned(t.env, candidate)
		if !ok {
			return t.f.Roots.Templates(t.env).Load(name)
		}
		if err == nil {
			return string(body), where, nil
		}
		if treesign.ReasonOf(err) != treesign.ReasonNotInManifest {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("%w: %s, in the signed revision of %s", template.ErrNotFound, name, t.env)
}
