package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no atomic replace that ignores every other opener. Rename
// below avoids the worst of it with POSIX semantics where it can (see
// replace); where it cannot, and for a reader that does not share delete,
// what follows still holds. MoveFileEx with MOVEFILE_REPLACE_EXISTING
// opens the destination for delete with no sharing, so:
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
// (TestAReplaceAndAReadDoNotBlockEachOther, DIVERGENCE 5.619). So any
// reader can cause the first, whatever sharing it asks for. Which part
// of the replace refuses a destination opened with FILE_SHARE_DELETE
// was not isolated; that it does was measured.
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
	return retry(func() error { return replace(oldpath, newpath) }, oldpath, newpath)
}

// replace is one attempt at the rename: with POSIX semantics where the
// filesystem has them, and as os.Rename -- MoveFileEx -- where it does not.
//
// MoveFileEx is the race in issue 799. Under it a replace is refused while
// anybody at all has the destination open, Read included, although Read
// shares delete: the old file is superseded rather than unlinked, and NTFS
// will not supersede a file with an open handle. The retry above waits that
// out, and on the windows-2022 race leg three quiet tries a few milliseconds
// apart did not (DIVERGENCE 5.619, 5.718).
//
// FILE_RENAME_POSIX_SEMANTICS asks for what rename(2) does instead: the old
// file leaves the namespace at once and lives on for whoever has it open,
// provided they opened it sharing delete, which Read does. So the commonest
// case the retry existed for -- this package's own reader -- is no longer a
// race at all. A reader that does not share delete (os.ReadFile, another
// program) still refuses the replace, and the retry is still what waits
// for it.
//
// os.Root.Rename already did this. Go's Windows Renameat asks for POSIX
// semantics and falls back when refused, so WriteIn, which renames through a
// root, never had the race that Write had: two paths in this package that
// should have agreed and did not.
//
// The flag needs Windows 10 1607 / Server 2016 and a filesystem that
// implements it; SPEC 27.1's oldest Windows is Server 2019, so in practice
// the fallback is for FAT and for network shares. It is tried afresh on
// every call rather than remembered, because one process can write to more
// than one filesystem and a remembered refusal would be a decision cached
// from one of them.
func replace(oldpath, newpath string) error {
	err := renamePOSIX(oldpath, newpath)
	if errors.Is(err, errNoPOSIXRename) {
		return os.Rename(oldpath, newpath)
	}
	return err
}

// errNoPOSIXRename is renamePOSIX declining: the source is a directory, or
// the filesystem or the Windows release does not do POSIX renames.
var errNoPOSIXRename = errors.New("POSIX rename semantics are not available here")

// setRenameInfo is the system call renamePOSIX makes. A variable so that a
// test can stand in for a filesystem that refuses it, which a CI runner's
// NTFS does not.
var setRenameInfo = windows.SetFileInformationByHandle

// fileRenameInfo is FILE_RENAME_INFO with the Flags arm of its union, which
// FileRenameInfoEx reads. x/sys/windows declares the class and the flags but
// not the structure.
type fileRenameInfo struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

// renamePOSIX renames oldpath over newpath with FILE_RENAME_POSIX_SEMANTICS,
// and reports errNoPOSIXRename when it cannot be asked for here.
//
// The errors it returns are shaped as os.Rename's are, *os.LinkError with op
// "rename", so that a caller's message and errors.Is tests do not depend on
// which of the two did the work.
func renamePOSIX(oldpath, newpath string) error {
	fail := func(err error) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	// Absolute, because a relative name in FILE_RENAME_INFO with no root
	// directory is not resolved the way the caller's working directory
	// would resolve it.
	target, err := filepath.Abs(newpath)
	if err != nil {
		return fail(err)
	}
	from, err := windows.UTF16PtrFromString(oldpath)
	if err != nil {
		return fail(err)
	}
	to, err := windows.UTF16FromString(target)
	if err != nil {
		return fail(err)
	}
	// The source shares everything, so that a reader who opens the new
	// name the moment the rename lands is not refused by this handle.
	h, err := windows.CreateFile(
		from,
		windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(h)
	// A directory goes the old way. The race is a reader of a file; what
	// POSIX semantics do to a directory renamed over another is a
	// different question, and the callers that move directories
	// (internal/extension, internal/gitfs) were written against
	// MoveFileEx's answer to it.
	var st windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &st); err != nil {
		return fail(err)
	}
	if st.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return errNoPOSIXRename
	}

	var zero fileRenameInfo
	size := unsafe.Offsetof(zero.FileName) + uintptr(len(to))*2
	if size < unsafe.Sizeof(zero) {
		size = unsafe.Sizeof(zero)
	}
	// Words rather than bytes, so the buffer has the structure's alignment.
	buf := make([]uint64, (size+7)/8)
	info := (*fileRenameInfo)(unsafe.Pointer(&buf[0]))
	// Not FILE_RENAME_IGNORE_READONLY_ATTRIBUTE: MoveFileEx refuses to
	// replace a read-only file, and so does this
	// (TestReplacingAReadOnlyFileFailsAtOnce).
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	// In bytes, and without the terminating NUL that `to` carries.
	info.FileNameLength = uint32((len(to) - 1) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(to)), to)

	err = setRenameInfo(h, windows.FileRenameInfoEx, (*byte)(unsafe.Pointer(info)), uint32(size))
	if err == nil {
		return nil
	}
	if posixRenameRefused(err) {
		return errNoPOSIXRename
	}
	return fail(err)
}

// posixRenameRefused reports whether err is the filesystem or the system
// declining FileRenameInfoEx or its POSIX flag, rather than refusing this
// rename. A fallback taken in error costs nothing worse than the
// MoveFileEx behaviour this replaced, so the test leans towards falling
// back. The codes are what a filesystem without the class answers; which
// of them FAT gives on a windows-2022 runner is recorded in DIVERGENCE
// 5.718.
func posixRenameRefused(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER) ||
		errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED)
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
