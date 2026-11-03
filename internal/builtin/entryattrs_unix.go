//go:build unix

package builtin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Changing the mode or owner of a file in a directory another account can
// write. DIVERGENCE 5.300 and M-29 closed the symbolic-link half of this:
// an os.Root will not leave its directory and Lchown will not follow a
// link. Neither says anything about a **hard link**, which is not followed
// because it is not a pointer at all: a second name for the same inode.
// An account that owns ~deploy could `ln /etc/spwd.db ~deploy/.profile`
// (FreeBSD's security.bsd.hardlink_check_uid is 0 by default, so any
// account may link a file it does not own), and the next highstate that
// set `mode:` or `group:` on ~deploy/.profile, or recursed over ~deploy, or
// re-applied an archive's `user:`, set it on spwd.db. As root.
//
// The rule, made in one place so that every writer below agrees:
//
//   - **A directory that only root, or the account halite runs as, can
//     write is trusted.** Nobody else could have put a name in it, so the
//     inode a name leads to is the file the state means, whatever else
//     links to it. The change is made by name, inside the held directory,
//     as before. Refusing every file with a second link would be wrong
//     twice: it would refuse /usr/local/bin/perl (a hard link to perl5.x on
//     FreeBSD), and it would let any account stop a state forever by
//     linking the file it manages into /tmp.
//
//   - **In any other directory, the entry is opened and the change made on
//     the descriptor**, after checking that it is the inode lstat saw, a
//     regular file or a directory, and, if a regular file, one with a
//     single link. The descriptor is what closes the swap: a check by name
//     and a change by name are two lookups, and the account owns what the
//     name means between them. A second link here is refused by name,
//     because there is no telling from the inode which of its names is the
//     one the account planted.
//
//   - **Anything else in such a directory is refused**: a symbolic link, a
//     socket, a FIFO, a device. None of them can be opened and changed
//     through a descriptor portably, and a change by name is the swap again.
//     Callers that meet these in a tree skip them with a warning rather
//     than fail the state; see archive.go and file_recurse.go.
//
// "Trusted" is read from the mode bits and the owner of the directory as
// it was opened. An access control list that grants another account the
// right to add entries without showing in the group bits (a named-user
// entry in an NFSv4 ACL on ZFS) is not seen; that is recorded in the
// ledger as the part this does not cover.

// entryChange is what to apply: a mode, an owner, or both. A uid or gid of
// -1 leaves that half alone, as chown(2) does. check makes every decision
// and changes nothing, which is how test mode reports the refusal the run
// would meet rather than a change it would not make.
type entryChange struct {
	mode     os.FileMode
	setMode  bool
	uid, gid int
	check    bool
}

func (c entryChange) setOwner() bool { return c.uid >= 0 || c.gid >= 0 }

// unsafeEntryError is a change refused because the name could lead
// somewhere the state does not manage. It is not a permission error, on
// purpose: writeAtomic tolerates a permission error from keepOwnership,
// and this must not be tolerated.
//
// byType is set when the entry was refused for what it is -- a link, a
// socket -- rather than for something that looks like an attack. A walk
// over a tree skips those with a warning: an archive that ships a symbolic
// link into a directory its `user:` owns is ordinary, and failing it on
// every run would be a regression for a link's owner, which grants
// nothing. A hard link or a swap fails the state.
type unsafeEntryError struct {
	path, why string
	byType    bool
}

func (e *unsafeEntryError) Error() string {
	return fmt.Sprintf("%s was not changed: %s", e.path, e.why)
}

// dirTrusted reports whether nobody but root and this process's account
// can add or replace an entry in dir.
func dirTrusted(dir *os.Root) (bool, error) {
	info, err := dir.Stat(".")
	if err != nil {
		return false, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, nil
	}
	owner := int(st.Uid)
	if owner != 0 && owner != os.Geteuid() {
		return false, nil
	}
	return info.Mode().Perm()&0o022 == 0, nil
}

// changeEntryIn applies c to rel, a path inside root. The directory that
// holds rel is opened as a root of its own, so that the last component is
// resolved once, inside a directory that can no longer change identity.
func changeEntryIn(root *os.Root, display, rel string, c entryChange) error {
	parent, base := filepath.Split(rel)
	if parent == "" {
		return changeEntryAt(root, display, base, c)
	}
	sub, err := root.OpenRoot(filepath.Clean(parent))
	if err != nil {
		return err
	}
	defer sub.Close()
	return changeEntryAt(sub, display, base, c)
}

// changeEntryPath is changeEntryIn for a full path. When follow is set the
// last component is resolved first, as chmod(2) resolves it, and the rule is
// then applied to the file the link leads to, in the directory that holds
// that file: a link is not a way around the check.
func changeEntryPath(path string, c entryChange, follow bool) error {
	if follow {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			path = resolved
		}
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return changeEntryAt(root, path, filepath.Base(path), c)
}

// changeEntryAt applies c to base, a single name inside dir.
func changeEntryAt(dir *os.Root, display, base string, c entryChange) error {
	trusted, err := dirTrusted(dir)
	if err != nil {
		return err
	}
	if trusted {
		if c.check {
			return nil
		}
		if c.setMode {
			if err := dir.Chmod(base, c.mode); err != nil {
				return err
			}
		}
		if c.setOwner() {
			return dir.Lchown(base, c.uid, c.gid)
		}
		return nil
	}

	seen, err := dir.Lstat(base)
	if err != nil {
		return err
	}
	if !seen.Mode().IsRegular() && !seen.IsDir() {
		return &unsafeEntryError{path: display, byType: true, why: fmt.Sprintf(
			"it is %s in a directory another account can write, and its mode and owner "+
				"cannot be changed there without a moment in which that account could "+
				"replace it with a hard link to another file", describeType(seen.Mode()))}
	}
	// O_NONBLOCK and O_NOCTTY in case the name became a FIFO or a
	// terminal after the lstat: the open must not wait, or take a
	// terminal, on behalf of the account that swapped it. SameFile below
	// refuses whatever it turned out to be.
	f, err := dir.OpenFile(base, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	got, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(seen, got) {
		return &unsafeEntryError{path: display,
			why: "it was replaced while it was being examined, in a directory another account can write"}
	}
	st, ok := got.Sys().(*syscall.Stat_t)
	if !ok {
		return &unsafeEntryError{path: display, why: "the number of its links could not be read"}
	}
	modeDone := !c.setMode || modeBits(got.Mode()) == modeBits(c.mode)
	ownerDone := (c.uid < 0 || int(st.Uid) == c.uid) && (c.gid < 0 || int(st.Gid) == c.gid)
	if modeDone && ownerDone {
		// Already right: a converged file is not refused for having a
		// second name, since nothing is about to be changed through it.
		return nil
	}
	if got.Mode().IsRegular() && uint64(st.Nlink) > 1 {
		return &unsafeEntryError{path: display, why: fmt.Sprintf(
			"it is a hard link (%d names for one file) in a directory another account can write, "+
				"so it may be another file entirely, linked there by that account; remove the "+
				"extra name, or manage the file at a path nobody else can write", st.Nlink)}
	}
	if c.check {
		return nil
	}
	if !modeDone {
		if err := f.Chmod(c.mode); err != nil {
			return err
		}
	}
	if !ownerDone {
		return f.Chown(c.uid, c.gid)
	}
	return nil
}

func describeType(m os.FileMode) string {
	switch {
	case m&os.ModeSymlink != 0:
		return "a symbolic link"
	case m&os.ModeSocket != 0:
		return "a socket"
	case m&os.ModeNamedPipe != 0:
		return "a FIFO"
	case m&os.ModeDevice != 0:
		return "a device"
	}
	return "not a regular file or a directory"
}

// skippedEntry reports whether err is a refusal a walk over a tree skips
// with a warning rather than fails on, and the warning to give.
func skippedEntry(err error) (string, bool) {
	var u *unsafeEntryError
	if errors.As(err, &u) && u.byType {
		return u.Error(), true
	}
	return "", false
}

// checkRecurseTarget asks changeEntryPath whether the change planned for t
// would be made, without making it.
func checkRecurseTarget(t recurseTarget, wantUser, wantGroup string) error {
	c := entryChange{uid: -1, gid: -1, check: true}
	if t.setMode {
		c.mode, c.setMode = t.mode, true
	}
	if t.setOwner {
		uid, gid, err := resolveOwner(wantUser, wantGroup)
		if err != nil {
			return err
		}
		c.uid, c.gid = uid, gid
	}
	return changeEntryPath(t.path, c, false)
}

// ownEntryIn gives rel, inside root, to wantUser and wantGroup, or with
// check set says whether it would.
func ownEntryIn(root *os.Root, rel, wantUser, wantGroup string, check bool) error {
	uid, gid, err := resolveOwner(wantUser, wantGroup)
	if err != nil {
		return err
	}
	return changeEntryIn(root, filepath.Join(root.Name(), rel), rel,
		entryChange{uid: uid, gid: gid, check: check})
}
