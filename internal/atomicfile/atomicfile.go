// Package atomicfile replaces a file's contents in one step, and reads a
// file that something else may be replacing at that moment.
//
// Six packages had their own copy of the same write-a-temp-file-and-
// rename helper. All six were correct on unix, where rename(2) is atomic
// and cannot fail because somebody has the destination open, and all six
// were wrong on Windows, where it can and does. The hub met it first:
// a delivery being recorded while the API served the same job produced
//
//	could not record a delivery  error=rename ...\.job.json.4094326449 ...\job.json: Access is denied.
//	reading the returns for ...  open ...\returns\web1.example.json: The process cannot access the file because it is being used by another process.
//
// which are the two sides of one race. MoveFileEx has to open the
// destination for delete with no sharing; a reader holding it open makes
// that fail, and while it holds it a reader's open fails instead. Neither
// error mentions the other party, and neither is recoverable by the
// caller, so both are handled here rather than at eighteen call sites.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces path with data, atomically as far as any reader is
// concerned: a reader sees either the old contents or the new, never a
// partial write and never an empty file.
//
// The temporary file is created in the destination's own directory,
// because a rename across filesystems is not atomic and on some is not
// permitted at all.
func Write(path string, data []byte, mode os.FileMode) error {
	return WritePrepared(path, data, mode, nil)
}

// WritePrepared is Write with one step inserted between the finished
// temporary file and the rename.
//
// It exists for one caller, and for a reason worth stating rather than
// generalising: internal/keystore has to hand its records to the account
// the store directory belongs to, and it has to do that *before* the
// rename, or the record is briefly reachable at its final path by the
// wrong account and is then left owned by the wrong one. There is no way
// to express that from outside this function, so keystore had its own
// copy of the whole helper -- and that copy had drifted in the two ways a
// copy does.
//
// It called `tmp.Chmod(mode)` rather than internal/fileperm, so on
// Windows a record written with mode 0600 got the read-only attribute and
// whatever ACL it inherited from its directory: the mode said private and
// the file was not. And it never synced the directory after the rename,
// so a power loss could leave a record with no name.
//
// `prepare` receives the temporary file's path and may be nil.
// DIVERGENCE 5.144.
//
// At most MaxConcurrentWrites of these run at once in a process, and
// the rest wait; see gate.go for why. `prepare` runs inside that bound,
// so it must not write through this package itself.
func WritePrepared(path string, data []byte, mode os.FileMode, prepare func(tmpPath string) error) error {
	return Bounded(func() error { return writePrepared(path, data, mode, prepare) })
}

func writePrepared(path string, data []byte, mode os.FileMode, prepare func(tmpPath string) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := chmodTemp(tmp, mode); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if beforeAttributes != nil {
		beforeAttributes(name)
	}
	// A file that is being replaced hands its extended attributes -- SELinux
	// context, POSIX ACLs, user attributes -- to its successor, before
	// `prepare` so that prepare has the last word. DIVERGENCE 5.492.
	//
	// On the open file, not on its name, and before the close. The
	// directory may be an account's, and the account can rename the
	// temporary name away and put a link to another file there at any
	// moment; a copy by name after the close put the ACL it chose, and the
	// caller's mode, on that other file (DIVERGENCE 5.566). The descriptor
	// is the file this function created whatever its name now says.
	if err := keepExtendedAttributes(tmp, path, mode); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Sync before the rename, so that a crash between the two leaves the
	// old file rather than a new one full of zeroes. After the attributes,
	// so that it covers them too.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if prepare != nil {
		if err := prepare(name); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	if err := Rename(name, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// The directory entry is synced too, so that the rename survives a
	// power loss rather than leaving a file with no name. Best effort:
	// a directory cannot be opened for reading on Windows, and a
	// filesystem that refuses the sync is not a reason to report a write
	// that succeeded as failed.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// beforeAttributes, when a test sets it, runs with the temporary file's
// name after its contents are written and before the replaced file's
// attributes are copied onto it -- while it is still open. It stands in for the account that owns
// the directory, which can rename that name away and put something else
// there at that moment (DIVERGENCE 5.566).
var beforeAttributes func(tmpName string)
