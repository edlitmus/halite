package atomicfile

// TEMPORARY, issue 799: probes of the raw primitive, never merged.

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type probeRenameInfo struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

// probePOSIXRename is SetFileInformationByHandle(FileRenameInfoEx) with
// REPLACE_IF_EXISTS|POSIX_SEMANTICS, returning the raw error.
func probePOSIXRename(oldpath, newpath string) error {
	from, err := windows.UTF16PtrFromString(oldpath)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(from, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	abs, err := filepath.Abs(newpath)
	if err != nil {
		return err
	}
	name, err := windows.UTF16FromString(abs)
	if err != nil {
		return err
	}
	var zero probeRenameInfo
	size := unsafe.Offsetof(zero.FileName) + uintptr(len(name))*2
	buf := make([]uint64, (size+7)/8)
	info := (*probeRenameInfo)(unsafe.Pointer(&buf[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.FileNameLength = uint32((len(name) - 1) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)
	return windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, (*byte)(unsafe.Pointer(info)), uint32(size))
}

func probeOnce(t *testing.T, label, dir string, holdShared bool) {
	from := filepath.Join(dir, label+".new")
	to := filepath.Join(dir, label+".old")
	if err := os.WriteFile(from, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var held windows.Handle
	if holdShared {
		name, _ := windows.UTF16PtrFromString(to)
		h, err := windows.CreateFile(name, windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Fatal(err)
		}
		held = h
	}
	errPOSIX := probePOSIXRename(from, to)
	var errno uintptr
	if e, ok := errPOSIX.(windows.Errno); ok {
		errno = uintptr(e)
	}
	got, _ := os.ReadFile(to)
	t.Logf("STRESS799 probe %-28s held-shared=%v: POSIX rename err=%v (errno %d); destination now %q", label, holdShared, errPOSIX, errno, got)
	if errPOSIX != nil {
		errMove := os.Rename(from, to)
		t.Logf("STRESS799 probe %-28s held-shared=%v: MoveFileEx err=%v", label, holdShared, errMove)
	}
	if holdShared {
		windows.CloseHandle(held)
	}
}

func TestStress799ProbePrimitive(t *testing.T) {
	if os.Getenv("HALITE_ATOMICFILE_STRESS") == "" {
		t.Skip("HALITE_ATOMICFILE_STRESS is not set; this measurement runs only on the issue 799 draft")
	}
	dir := t.TempDir()
	probeOnce(t, "ntfs", dir, false)
	probeOnce(t, "ntfs-reader", dir, true)
	// MoveFileEx with the same reader, for comparison.
	from := filepath.Join(dir, "mv.new")
	to := filepath.Join(dir, "mv.old")
	os.WriteFile(from, []byte("new"), 0o600)
	os.WriteFile(to, []byte("old"), 0o600)
	name, _ := windows.UTF16PtrFromString(to)
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("STRESS799 probe MoveFileEx with a shared reader held: %v", os.Rename(from, to))
	windows.CloseHandle(h)
}

func probeFATPrimitive(t *testing.T, dir string) {
	probeOnce(t, "fat", dir, false)
	probeOnce(t, "fat-reader", dir, true)
}
