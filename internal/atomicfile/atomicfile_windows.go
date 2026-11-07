package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Windows has no atomic replace that ignores other openers. MoveFileEx
// with MOVEFILE_REPLACE_EXISTING opens the destination for delete with
// no sharing, so:
//
//   - a reader holding the destination open makes the rename fail with
//     ERROR_ACCESS_DENIED, and
//   - while the rename holds it, another open of the destination fails
//     with ERROR_SHARING_VIOLATION.
//
// Both are transient by construction: the other party is mid-read or
// mid-rename and will be finished in microseconds. Read requests
// FILE_SHARE_DELETE so that it never causes the first, and both retry
// briefly for the case where the other party is code that does not,
// which includes every os.ReadFile in the standard library.
//
// The first is only transient when it means a reader. ERROR_ACCESS_DENIED
// is also what a real refusal returns -- a read-only destination, a deny
// entry, a directory this account may not write -- and those were
// retried too, so every caller that met one waited out the whole window
// before it was told (issue 614, DIVERGENCE 5.619). See heldOpen.

// retryWindow is how long a caller waits for the other party to let go.
// Long enough to cover a read of a job file on a loaded machine, short
// enough that a genuinely locked file — an antivirus scanner, a
// half-closed handle — is reported rather than hung on.
const retryWindow = 2 * time.Second

// transient reports whether an error is one of the two sharing failures
// that mean "somebody else has it open right now" rather than "you may
// not do this". paths are the files the failed operation touched.
func transient(err error, paths ...string) bool {
	if errors.Is(err, syscall.Errno(windows.ERROR_SHARING_VIOLATION)) {
		return true
	}
	if !errors.Is(err, syscall.Errno(windows.ERROR_ACCESS_DENIED)) {
		return false
	}
	for _, p := range paths {
		if heldOpen(p) {
			return true
		}
	}
	return false
}

// heldOpen reports whether somebody has path open in a way that stops it
// being deleted or replaced.
//
// It asks by opening the path for DELETE with every sharing flag set. If
// another handle was opened without FILE_SHARE_DELETE -- os.Open's way --
// this open fails with ERROR_SHARING_VIOLATION, and that is the reader
// the retry is for. A permission refusal fails with ERROR_ACCESS_DENIED,
// and a read-only file or a missing one does not fail with a sharing
// violation at all, so none of them is waited on.
//
// The probe shares everything, so it cannot itself become the reason a
// concurrent writer's replace fails: an opener that shares delete does
// not stop a replace, which is what Read relies on.
//
// One transient denial this cannot see: a file in the delete-pending
// state, deleted while somebody still holds it, also refuses an open
// with ERROR_ACCESS_DENIED. Nothing in halite deletes a file it is
// replacing, so it is reported at once rather than waited for.
func heldOpen(path string) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(
		name,
		windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		// A directory cannot be opened without it, and a directory is a
		// path a rename can be refused over.
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err == nil {
		windows.CloseHandle(h)
		return false
	}
	return errors.Is(err, syscall.Errno(windows.ERROR_SHARING_VIOLATION))
}

// retry runs op until it stops failing transiently or the window closes.
// The last error is returned unwrapped, so a caller that is not racing
// anybody sees exactly the error the operation gave.
func retry(op func() error, paths ...string) error {
	deadline := time.Now().Add(retryWindow)
	backoff := time.Millisecond
	for {
		err := op()
		if err == nil || time.Now().After(deadline) || !transient(err, paths...) {
			return err
		}
		time.Sleep(backoff)
		if backoff < 32*time.Millisecond {
			backoff *= 2
		}
	}
}

// Rename replaces newpath with oldpath, waiting out a reader that has
// newpath open.
func Rename(oldpath, newpath string) error {
	return retry(func() error { return os.Rename(oldpath, newpath) }, oldpath, newpath)
}

// Read reads a file that something else may be replacing at the same
// moment.
//
// os.ReadFile does not ask for FILE_SHARE_DELETE, so a file it has open
// cannot be renamed over: reading a job's returns would make recording
// the next one fail. This opens with all three sharing flags, which
// costs nothing and takes this process out of the way of every writer.
func Read(path string) ([]byte, error) {
	var out []byte
	err := retry(func() error {
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		h, err := windows.CreateFile(
			name,
			windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if err != nil {
			// Reported as though os.Open had failed, so that a caller
			// testing for fs.ErrNotExist still recognises it.
			return &os.PathError{Op: "open", Path: path, Err: err}
		}
		f := os.NewFile(uintptr(h), path)
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		out = b
		return nil
	}, path)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// renameIn is Rename inside a root, with the same wait for a reader that
// has newname open. See WriteIn.
func renameIn(root *os.Root, oldname, newname string) error {
	return retry(func() error { return root.Rename(oldname, newname) },
		filepath.Join(root.Name(), oldname), filepath.Join(root.Name(), newname))
}
