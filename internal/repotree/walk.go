package repotree

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Finding the repository, and walking it, in one place.
//
// # Why these exist
//
// This package's own comment records half of a fault and fixed half of it:
// "Each walker kept its own list of directories to skip, and the lists had
// already drifted apart." `OtherCheckout` fixed the part about a second
// checkout. The lists stayed drifted, and a later count found **six different
// spellings** across seventeen walkers —
//
//	.git vendor bin dist testdata contrib
//	.git bin dist vendor testdata
//	.git vendor bin dist testdata
//	.git vendor bin dist
//	.git bin testdata
//
// — which means the audits that each claim to read "the whole tree" read
// different trees, and nobody could say which. `TestNothingClaimsADeliveredPhase`
// skipped neither `vendor` nor `dist`, so it read 511 files where the repository
// has 483: it had been auditing `golang.org/x/sys` for claims about halite's
// delivery phases.
//
// The root was found three ways too. `filepath.Join("..", "..")` in eleven
// places, and three byte-identical copies of a helper that walks up to `go.mod`
// — in `internal/buildpolicy`, `internal/chaos` and `internal/exec`, the last
// under a different name. A fourth would have been written next.
//
// DIVERGENCE 5.166.

// skipped are the directory names a repository walk does not enter.
//
// `.git` is git's own; `vendor` is somebody else's code, which this project may
// not edit and must not audit; `bin` and `dist` are build output; `testdata`
// holds fixtures that are deliberately wrong, which is the whole point of them
// and the reason an audit reading one reports a defect that is a fixture; and
// `contrib` is shell, HCL and YAML rather than this module's source.
//
// One list. A walker that wants a different one is asking a different question
// and should say so in its own words rather than by omitting an entry here.
var skipped = map[string]bool{
	".git":     true,
	"vendor":   true,
	"bin":      true,
	"dist":     true,
	"testdata": true,
	"contrib":  true,
}

// Root is the repository root, found by walking up from the working directory
// to the directory holding go.mod.
//
// A test's working directory is its own package's, so every audit needs this or
// a hand-counted `filepath.Join("..", "..")` — which is right until somebody
// moves the package one level, and which cannot be checked by anything.
//
// It returns an error rather than taking a `*testing.T`, so that a non-test
// caller can use it: `internal/buildpolicy`'s lexicon scan is not a test.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s, so the repository root cannot be found", dir)
		}
		dir = parent
	}
}

// Walk is filepath.WalkDir over a repository, entering only the directories
// that hold this project's own source.
//
// `fn` sees files and the directories that were not skipped, exactly as
// `filepath.WalkDir` would — so a caller converted to this keeps working
// whether or not it inspects directories, and the `if d.IsDir()` block it used
// to need to do the skipping can go.
//
// A directory that is a separate git work tree is skipped too. That is
// `OtherCheckout`'s job and the reason this package exists; putting it here
// means a new walker gets it without knowing it had to ask.
func Walk(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fn(path, d, err)
		}
		if d.IsDir() {
			if skipped[d.Name()] || OtherCheckout(root, path) {
				return fs.SkipDir
			}
		}
		return fn(path, d, nil)
	})
}
