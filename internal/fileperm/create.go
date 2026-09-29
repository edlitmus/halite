package fileperm

import (
	"fmt"
	"os"
)

// Making a directory or a file that other accounts must not reach.
//
// # Why these exist rather than a mode at each call site
//
// `os.MkdirAll(dir, 0o700)` and `os.WriteFile(path, data, 0o600)` are how a Go
// program says "only this account", and on Windows they say nothing: a
// directory mode is not an access control decision there at all, and a file
// mode is the read-only attribute. Thirty-one places in this repository said
// it that way. Each was one line of intent that the platform discarded, and
// `internal/pki` was the one where that was visibly worst -- the enrollment CA's
// private key (DIVERGENCE 5.161).
//
// Fixing them one at a time meant repeating a `MkdirAll` and an `Apply` in
// thirty-one places, which is thirty-one chances to do only the first half. So
// the pair is one call, the mode is still stated at the call site because that
// is where the intent belongs, and `nocreates_test.go` fails the build if a
// thirty-second site says it the old way.
//
// **The mode is not ignored on Windows.** `Apply` chmods as well as setting the
// list, because a caller that asked for a file with no write bits meant that
// too, and something may be reading the mode back.
//
// DIVERGENCE 5.164.

// MkdirAll makes a directory and everything above it, then restricts the
// directory itself if anything beyond its owner can reach it.
//
// Only the leaf is restricted, and deliberately: `MkdirAll` may have created
// parents, and those belong to whoever laid out the tree rather than to the
// caller asking for one directory inside it. A caller that wants a whole
// private path asks for each level.
func MkdirAll(path string, mode os.FileMode) error {
	_, statErr := os.Stat(path)
	existed := statErr == nil
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	return restrict(path, mode, existed)
}

// OpenFile opens a file and restricts it to the accounts the mode allows.
//
// For the callers that need the handle rather than a finished file: a log the
// process appends to for its lifetime, an evidence segment, a download written
// through io.Copy. `os.OpenFile`'s mode argument applies only when the call
// creates the file, so a path that already existed kept whatever it had --
// which on a long-lived log is the file most likely to have been created by
// something else.
//
// The restriction is applied to the open handle, before the caller writes
// anything through it. A failure closes the file rather than handing back one
// that is open and not private, because a caller that ignored the error would
// then write the secret into it.
func OpenFile(path string, flag int, mode os.FileMode) (*os.File, error) {
	_, statErr := os.Stat(path)
	existed := statErr == nil
	f, err := os.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	// **A mode means nothing to an open that cannot create the file**, and
	// acting on it anyway is worse than useless. Go ignores the argument for a
	// non-creating open and the convention is to pass 0; a caller that is only
	// reading has no business changing who can read.
	//
	// It is also not permitted. The `test (windows-2022)` leg found this: an
	// O_RDONLY handle cannot carry out `Chmod`, so `ApplyFile` came back
	//
	//	restricting …\sealed: chmod …\sealed: Access is denied.
	//
	// and a helper meant to make a file private failed on one that already was.
	// `nocreates_test.go` applies the same rule when deciding what to report --
	// one rule in two places, and they have to agree, which is the shape of
	// most of this chapter.
	if flag&os.O_CREATE == 0 {
		return f, nil
	}
	if existed {
		reachable, err := others(path)
		if err != nil {
			f.Close()
			return nil, err
		}
		if !reachable {
			return f, nil
		}
	}
	if err := ApplyFile(f, mode); err != nil {
		f.Close()
		return nil, fmt.Errorf("restricting %s: %w", path, err)
	}
	return f, nil
}

// WriteFile writes a file and restricts it if anything beyond its owner can
// reach it.
//
// Not atomic: use `atomicfile.Write` where a reader must never see a partial
// file. This is for the cases `os.WriteFile` was already right for -- a spool
// entry nothing has been told about yet, a probe written and immediately
// removed -- with the permission carried out on both platforms.
//
// The restriction is applied after the write rather than before, because
// `os.WriteFile` opens the path itself. A caller that cannot tolerate the
// window between the two wants `atomicfile.Write`, which restricts a temporary
// file before it has the final name.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	_, statErr := os.Stat(path)
	existed := statErr == nil
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return restrict(path, mode, existed)
}

// restrict applies the mode, unless the path was already there and already
// private.
//
// # Why "already private" is left alone rather than set again
//
// `Apply` is a chmod, and a chmod **widens** as readily as it narrows. The
// first version of these helpers applied it unconditionally, and two tests in
// other packages failed at once: both make a directory mode 0500 and assert
// that opening a store there is refused, because the store probes by writing a
// file. `Apply(dir, 0o700)` chmod'd it back to writable -- so the probe
// succeeded, the refusal never came, and a helper written to protect a
// directory had quietly undone a restriction somebody chose.
//
// That is worth more than the two tests. An operator who sets an evidence
// directory to 0500 means it, and a configuration management system that
// reopens it on every start is doing the opposite of its job. Those tests were
// written for a different reason -- DIVERGENCE 5.20, a directory left owned by
// root that made every target match nothing --
// and they caught this because they assert on a *deliberately* tight
// permission, which nothing else here does.
//
// So the question asked is `Others`, which is this package's own: is anybody
// beyond the owner able to reach it? If yes, narrow it. If no, leave it exactly
// as it is, tighter or not. A path this call **created** is always restricted,
// whatever its mode came out as, because on Windows a new directory inherits
// its parent's list and "nobody else can reach it today" is not the same as a
// list that says so.
func restrict(path string, mode os.FileMode, existed bool) error {
	if existed {
		reachable, err := others(path)
		if err != nil {
			return err
		}
		if !reachable {
			return nil
		}
	}
	if err := Apply(path, mode); err != nil {
		return fmt.Errorf("restricting %s: %w", path, err)
	}
	return nil
}

// others is Others as a question rather than a list.
func others(path string) (bool, error) {
	who, err := Others(path)
	if err != nil {
		return false, fmt.Errorf("reading who can reach %s: %w", path, err)
	}
	return len(who) > 0, nil
}
