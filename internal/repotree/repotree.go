// Package repotree answers one question for the audits that walk this
// repository: is a directory part of the tree being audited, or a
// different checkout that happens to sit inside it?
//
// The audits walk from the repository root and read every file they
// find. A git worktree made inside the checkout -- `.claude/worktrees/`
// is where Claude Code puts them, and nothing stops anyone putting one
// anywhere else -- is a whole second copy of the repository, at another
// commit. TestNoMathRand found one and reported the template seed's two
// files as offenders, because their paths were no longer the two it
// allows; every other audit read the second copy too, and passed or
// failed on content that is not this tree's. Each walker kept its own
// list of directories to skip, and the lists had already drifted apart.
//
// The rule is git's own: a directory below the root with a `.git` entry
// of its own is a separate work tree. A worktree has a `.git` file; a
// nested clone has a `.git` directory; either is enough.
package repotree

import (
	"os"
	"path/filepath"
)

// OtherCheckout reports whether dir, a directory at or below root, is a
// separate git work tree. The root itself never is.
func OtherCheckout(root, dir string) bool {
	if filepath.Clean(dir) == filepath.Clean(root) {
		return false
	}
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
