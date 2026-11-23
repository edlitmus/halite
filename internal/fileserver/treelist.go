package fileserver

import (
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/treesign"
)

// TreeEntries is an environment as the hub would list it, in the form a
// signed manifest names it: everything Roots.Manifest serves except
// `_halite/` itself, with each file's mode.
//
// Built on Roots.Manifest, the function the hub answers listings with, so
// the signer and the hub share one listing rule, symlink containment and
// the hide rules included. A listing that two functions produced would be
// two things to keep in step, and the first file they disagreed about
// would be refused on every node.
func (r *Roots) TreeEntries(env, hashType string) ([]treesign.Entry, error) {
	listed, err := r.Manifest(env, "", hashType)
	if err != nil {
		return nil, err
	}
	if listed.Truncated {
		return nil, fmt.Errorf("the tree holds more than %d files, which this version cannot sign", treesign.MaxEntries)
	}
	out := []treesign.Entry{}
	for _, e := range listed.Files {
		if treesign.IsReserved(e.Path) {
			continue
		}
		resolved, err := r.Resolve(env, e.Path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, err
		}
		out = append(out, treesign.Entry{Path: e.Path, Hash: e.Hash, Size: e.Size,
			Mode: treesign.FormatMode(uint32(info.Mode().Perm()))})
	}
	return out, nil
}
