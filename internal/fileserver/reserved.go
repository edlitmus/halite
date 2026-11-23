package fileserver

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/edlitmus/halite/internal/treesign"
)

// ReservedProblem reports what is wrong with an environment's `_halite/`
// directory, or nil when there is nothing wrong.
//
// `_halite/` is reserved for the signed manifest and its signature (issue
// 764), and holds nothing else. The hub refuses to serve an environment
// where it does, naming the file, because a reserved directory that can
// hold anything is a place for content the manifest does not describe,
// and a reader who knows the rule would not look there for it. The
// maintainer asked for the refusal (proposal 10.7).
//
// Every root of the environment is checked, not only the one that would
// win a lookup: a later root's `_halite/` is shadowed for serving and is
// still in the tree somebody signs.
func (r *Roots) ReservedProblem(env string) error {
	dirs, ok := r.dirsFor(env)
	if !ok {
		return nil
	}
	for _, dir := range dirs {
		root, err := realRoot(dir)
		if err != nil {
			continue
		}
		reserved := filepath.Join(root, treesign.ReservedDir)
		info, err := os.Lstat(reserved)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("environment %s: %s cannot be read: %w", env, reserved, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("environment %s: %s is not a directory; %s/ is reserved for the signed "+
				"manifest and its signature", env, reserved, treesign.ReservedDir)
		}
		entries, err := os.ReadDir(reserved)
		if err != nil {
			return fmt.Errorf("environment %s: %s cannot be read: %w", env, reserved, err)
		}
		for _, e := range entries {
			name := e.Name()
			if (name == "manifest.json" || name == "manifest.sig") && e.Type().IsRegular() {
				continue
			}
			return fmt.Errorf("environment %s: %s holds %s, and %s/ is reserved for manifest.json and "+
				"manifest.sig alone, as regular files; move it out of the tree's %s/",
				env, reserved, name, treesign.ReservedDir, treesign.ReservedDir)
		}
	}
	return nil
}

// HidesManifest reports whether file_ignore_glob or file_ignore_regex
// hides the signed manifest, which makes every node that requires one
// refuse the tree with "serves no signed manifest".
func (r *Roots) HidesManifest() bool {
	return r.ignored("/"+treesign.ManifestPath) || r.ignored("/"+treesign.SignaturePath)
}
