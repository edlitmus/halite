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
// mid-rename and will be finished in microseconds, so both are retried
// briefly.
//
// This said that Read, because it asks for FILE_SHARE_DELETE, never
// causes the first. The windows-2022 runner showed otherwise: with
// Read holding the destination, the replace still failed with
// ERROR_ACCESS_DENIED, and only the retry had been hiding it
// (TestAReplaceAndAReadDoNotBlockEachOther, DIVERGENCE 5.618). So any
// reader can cause the first, whatever sharing it asks for. Which part
// of the replace refuses a destination opened with FILE_SHARE_DELETE
// was not isolated; that it does was measured.
//
// The first is only transient when it means a reader. ERROR_ACCESS_DENIED
// is also what a real refusal returns -- a read-only destination, a deny
// entry, a directory this account may not write -- and those were
// retried too, so every caller that met one waited out the whole window
// before it was told (issue 614, DIVERGENCE 5.618). See heldOpen.

// retryWindow is how long a caller waits for the other party to let go.
// Long enough to cover a read of a job file on a loaded machine, short
// enough that a genuinely locked file — an antivirus scanner, a
// half-closed handle — is reported rather than hung on.
const retryWindow = 2 * time.Second

// verdict is what retry does with one failure.
type verdict int

const (
	permanent   verdict = iota // report it
	transient                  // somebody has it open: wait for them
	unexplained                // denied, and nobody holds it now
)

// classify sorts a failure. paths are the files the operation touched.
func classify(err error, paths ...string) verdict {
	if errors.Is(err, syscall.Errno(windows.ERROR_SHARING_VIOLATION)) {
		return transient
	}
	if !errors.Is(err, syscall.Errno(windows.ERROR_ACCESS_DENIED)) {
		return permanent
	}
	for _, p := range paths {
		if heldOpen(p) {
			return transient
		}
	}
	return unexplained
}

// quietDenials is how many times a denial with nobody holding the file
// is tried again before it is believed.
//
// The probe runs after the failure, so a reader that let go in between
// leaves a denial the probe cannot explain. The race leg on the
// windows-2022 runner lost that race in TestAReplaceAndAReadDoNotBlockEachOther
// with the probe alone. A real refusal fails the same way every time, so
// three more tries a few milliseconds apart cost it under ten
// milliseconds, against the two seconds it cost before.
const quietDenials = 3

// heldOpen reports whether somebody else has path open.
//
// It asks by opening the path for DELETE and sharing nothing. Any other
// handle on the file, whatever sharing it asked for, makes that open
// fail with ERROR_SHARING_VIOLATION, and that is the reader the retry is
// for. A permission refusal fails with ERROR_ACCESS_DENIED, and a
// read-only file or a missing one does not fail with a sharing violation
// at all, so none of them is waited on.
//
// The first version of this probe shared everything, so it saw only a
// reader that had not shared delete -- os.Open's way -- and missed one
// that had, which is Read. The runner caught it the same day.
//
// The probe holds the file for the length of one open and close. A
// concurrent replace or Read that meets it gets a sharing failure,
// which is transient here, and waits it out like any other.
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
		0,
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
	quiet := 0
	for {
		err := op()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		switch classify(err, paths...) {
		case permanent:
			return err
		case unexplained:
			if quiet >= quietDenials {
				return err
			}
			quiet++
		case transient:
			// A reader seen is a reader active, so a denial that
			// follows is more likely the next one than a refusal.
			quiet = 0
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
