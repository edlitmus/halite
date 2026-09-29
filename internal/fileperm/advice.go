package fileperm

import (
	"fmt"
	"os"
)

// The text of Advice, for both platforms, in a file that compiles on both.
//
// # Why the strings are here and not beside Apply
//
// Every other function in this package is split by build tag, because each has
// a real implementation per platform and only one of them can exist. Advice has
// no implementation: it is two pieces of text, and putting each beside the
// platform it describes meant the Windows wording could be checked only on a
// Windows machine. A wrong icacls flag in a string nothing executes is the kind
// of thing that sits unnoticed for a year, which is roughly what happened.
//
// So the wording lives here, taking the platform's own question -- is this a
// directory? -- as a parameter, and each platform's Advice picks the one that
// belongs to it. `venvPip` and `linkZone` took the same shape for the same
// reason: a branch no test on the running machine can reach is a branch nobody
// has read since it was written. DIVERGENCE 5.163.

// unixAdvice is the chmod to run.
//
// **A directory takes 700 and not 600.** The execute bit on a directory is what
// permits traversal, so 600 makes it unreachable by its own owner rather than
// private -- an operator told that about a key directory would follow it and end
// up with one the hub itself cannot read, which is worse than where they
// started.
func unixAdvice(path string, isDir bool) string {
	if isDir {
		return fmt.Sprintf("chmod 700 %s", path)
	}
	return fmt.Sprintf("chmod 600 %s", path)
}

// windowsAdvice is the icacls to run.
//
// **A directory takes the inheriting form**, which is what `RestrictDir` sets
// and `Restrict` does not: `(OI)(CI)` is icacls's spelling of
// SUB_CONTAINERS_AND_OBJECTS_INHERIT. Without it the restriction stops at the
// directory itself and a file written into it afterwards picks up whatever the
// parent above would have given it -- so an operator following the advice would
// get a private directory holding keys that are not.
//
// The advice and Apply are two descriptions of one operation, which is the
// commonest defect shape in this repository, so the difference between them is
// the same difference: one flag, in the two spellings the platform has for it.
func windowsAdvice(path string, isDir bool) string {
	grant := `"%USERNAME%:F" SYSTEM:F Administrators:F`
	if isDir {
		grant = `"%USERNAME%:(OI)(CI)F" SYSTEM:(OI)(CI)F Administrators:(OI)(CI)F`
	}
	return fmt.Sprintf(`icacls "%s" /inheritance:r /grant:r %s`, path, grant)
}

// adviceIsDir reports whether Advice should describe a directory.
//
// # Why this is read from the filesystem and not passed in
//
// Both callers ask immediately after `Others` has already stat'd the path and
// found accounts that should not reach it, so the path is there. Passing the
// fact instead would put the decision at every call site -- and a caller that
// has just been handed a path from configuration does not know what is at the
// end of it any more than this does.
//
// A path that has gone away between the two calls falls back to the file form.
// That is the common case, and advice about a path that no longer exists is
// advice nobody can follow whichever form it takes.
func adviceIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
