package fileserver

import (
	"errors"
	"fmt"

	"github.com/edlitmus/halite/internal/treesign"
)

// signedRevision is this run's accepted manifest for an environment of
// the hub's tree.
func (r *Remote) signedRevision(env string) (*Revision, error) {
	return r.Guard.revision(r.run, env, r.fetchSigned)
}

// fetchSigned fetches `_halite/manifest.json` and `_halite/manifest.sig`
// with the transport call every file read uses, bypassing the file cache
// and the hub's digest for them, neither of which is any part of trusting
// them. When no hub answers, the manifest this node last accepted for the
// environment is the answer, and offline says so.
func (r *Remote) fetchSigned(env string) ([]byte, []byte, bool, error) {
	if r.Unreachable != nil && r.holdingOff() {
		return r.savedSigned(env, errors.New("the hub cannot be reached"))
	}
	var bodies [2][]byte
	for i, rel := range []string{treesign.SignaturePath, treesign.ManifestPath} {
		body, _, _, err := r.Client.FetchFile(r.context(), env, rel, "")
		if err != nil {
			if r.unreachable(err) {
				r.noteUnreachable(err)
				return r.savedSigned(env, err)
			}
			return nil, nil, false, err
		}
		bodies[i] = body
	}
	r.noteAnswered()
	return bodies[1], bodies[0], false, nil
}

func (r *Remote) savedSigned(env string, cause error) ([]byte, []byte, bool, error) {
	manifest, sig, err := r.Guard.Records.Saved(env)
	if err != nil {
		return nil, nil, true, cause
	}
	return manifest, sig, true, nil
}

// cacheSigned is cacheFile for a node that requires signed state content.
//
// A path the manifest does not name is not found, whatever the hub would
// say about it. A path it names is asked for with the manifest's digest
// as the entity tag, so an unchanged file costs a 304 as it does today,
// and a body that comes back must hash to the manifest's digest before it
// is kept. The verified body lives in the content-addressed store, and
// the path returned is there, so two runs at different revisions cannot
// overwrite each other's file between the check and the use.
func (r *Remote) cacheSigned(env, rel string) (string, error) {
	rev, err := r.signedRevision(env)
	if err != nil {
		return "", err
	}
	if rev == nil {
		return r.cacheUnsigned(env, rel)
	}
	entry, ok := rev.Manifest.Lookup(rel)
	if !ok {
		refusal := notInRevision(env, rel, rev)
		if r.Guard.Mode == SignatureWarn {
			// Counted only when the hub has it: what `true` would have
			// refused is a file that exists, not a name nobody published.
			path, err := r.cacheUnsigned(env, rel)
			if err == nil {
				r.Guard.note(refusal)
			}
			return path, err
		}
		r.Guard.note(refusal)
		return "", refusal
	}
	object, have := r.Guard.haveObject(r.cache, rev, entry)
	etag := ""
	if have {
		etag = rev.Manifest.HashType + ":" + entry.Hash
	}
	if rev.Offline || r.Unreachable != nil && r.holdingOff() {
		if have {
			return object, nil
		}
		return "", treesign.Refuse(treesign.ReasonOffline, env, rel,
			"the hub cannot be reached and this node has no verified copy of %s/%s at revision %s",
			env, rel, rev.ID)
	}
	body, published, notModified, err := r.Client.FetchFile(r.context(), env, rel, etag)
	if err != nil {
		if r.unreachable(err) {
			r.noteUnreachable(err)
			if have {
				return object, nil
			}
		}
		return "", err
	}
	r.noteAnswered()
	if notModified {
		if !have {
			return "", fmt.Errorf("the hub answered %s/%s as not modified to a request that named no copy", env, rel)
		}
		return object, nil
	}
	kept, err := r.Guard.keep(r.cache, env, rev, entry, body, published)
	if err != nil {
		var refusal *treesign.Refusal
		if errors.As(err, &refusal) {
			if err := r.Guard.refuse(refusal); err != nil {
				return "", err
			}
			return r.cacheUnsigned(env, rel)
		}
		return "", err
	}
	return kept, nil
}
