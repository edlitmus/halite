//go:build linux

package atomicfile

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// aclXattr is a `system.posix_acl_access` value in the kernel's own format:
// a version word, then one (tag, perm, id) entry per line of `getfacl`, in
// tag order. Built by hand so that these tests need no setfacl, which the
// fleet image does not have; the format is the one
// include/uapi/linux/posix_acl_xattr.h fixes. The swap tests fail on the old
// code with this ACL on the victim, so the kernel took it.
func aclXattr(namedUID uint32) []byte {
	type entry struct {
		tag, perm uint16
		id        uint32
	}
	const undefined = 0xffffffff
	entries := []entry{
		{0x01, 6, undefined}, // user::rw-
		{0x02, 6, namedUID},  // user:<uid>:rw-
		{0x04, 4, undefined}, // group::r--
		{0x10, 6, undefined}, // mask::rw-
		{0x20, 4, undefined}, // other::r--
	}
	b := binary.LittleEndian.AppendUint32(nil, 2)
	for _, e := range entries {
		b = binary.LittleEndian.AppendUint16(b, e.tag)
		b = binary.LittleEndian.AppendUint16(b, e.perm)
		b = binary.LittleEndian.AppendUint32(b, e.id)
	}
	return b
}

// plantAttributes gives path a user attribute and an ACL, the two things an
// account that owns the file can put on it, and skips when the filesystem
// takes neither.
func plantAttributes(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Setxattr(path, "user.halite-planted", []byte("chosen"), 0); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EPERM) {
			t.Skipf("this filesystem takes no user extended attributes: %v", err)
		}
		t.Fatal(err)
	}
	if err := syscall.Setxattr(path, "system.posix_acl_access", aclXattr(uint32(os.Getuid())), 0); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) {
			t.Skipf("this filesystem takes no ACLs: %v", err)
		}
		t.Fatal(err)
	}
}

// untouched fails if victim gained an extended attribute or changed mode.
func untouched(t *testing.T, victim string, mode os.FileMode) {
	t.Helper()
	if n, err := syscall.Listxattr(victim, nil); err == nil && n > 0 {
		buf := make([]byte, n)
		n, _ = syscall.Listxattr(victim, buf)
		t.Errorf("the file outside the write gained extended attributes: %q", splitNul(buf[:n]))
	}
	fi, err := os.Lstat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != mode {
		t.Errorf("the file outside the write went from %v to %v", mode, fi.Mode().Perm())
	}
}

// The replaced file's attributes are copied onto the temporary file, not onto
// whatever its name points at by then.
//
// The temporary file sits in the target's directory. When that directory is
// an account's (`file.managed` on `~deploy/.profile`, run as root), the
// account owns the replaced file, so it chooses the ACL that gets copied, and
// it can rename the temporary name away and put a symbolic or hard link to any
// file there. The copy was made by name after the close -- `setxattr` and then
// `chmod`, both of which follow a symbolic link -- so the ACL the account
// chose, and the mode the caller asked for, landed on the other file. Against
// /etc/shadow that is an entry granting the account read and write.
//
// The test is its own "other account": everything here is ours, so the
// setxattr on the victim succeeds the way root's would. The swap is made at
// the one moment the account needs, through beforeAttributes, rather than by
// a racing goroutine that would usually lose.
func TestWriteDoesNotCopyAttributesThroughASwappedTemporaryName(t *testing.T) {
	for _, swap := range []struct {
		name string
		make func(victim, at string) error
	}{
		{"symbolic link", os.Symlink},
		{"hard link", os.Link},
	} {
		t.Run(swap.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".profile")
			if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			plantAttributes(t, path)
			victim := filepath.Join(t.TempDir(), "shadow")
			if err := os.WriteFile(victim, []byte("secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			swapped := false
			beforeAttributes = func(tmp string) {
				if err := os.Remove(tmp); err != nil {
					t.Error(err)
					return
				}
				if err := swap.make(victim, tmp); err != nil {
					t.Error(err)
					return
				}
				swapped = true
			}
			defer func() { beforeAttributes = nil }()

			// Whether the write reports success is not the point: what the
			// name was swapped for is renamed into place, and that is the
			// account's own business in its own directory.
			_ = Write(path, []byte("new\n"), 0o644)
			if !swapped {
				t.Fatal("the swap never ran, so nothing was tested")
			}
			untouched(t, victim, 0o600)
			if got, _ := os.ReadFile(victim); string(got) != "secret\n" {
				t.Errorf("the file outside the write was rewritten: %q", got)
			}
		})
	}
}

// The replaced file is read through a descriptor that will not follow a
// symbolic link, and nothing that is not a regular file is read at all.
//
// The account that owns the directory can swap the replaced name at any
// moment: for a symbolic link to a file whose attributes it wants copied onto
// the file being written (an SELinux label, an ACL, `user.*` values it cannot
// read), for a FIFO, which a blocking open would wait on for ever, or for a
// directory. WritePrepared used to lstat the name first, which only moved the
// window. This calls keepExtendedAttributes directly with the name already
// swapped, which is the moment that matters.
func TestKeepExtendedAttributesReadsOnlyTheReplacedFileItself(t *testing.T) {
	for _, swap := range []struct {
		name string
		make func(t *testing.T, source, at string)
	}{
		{"symbolic link", func(t *testing.T, source, at string) {
			if err := os.Symlink(source, at); err != nil {
				t.Fatal(err)
			}
		}},
		{"FIFO", func(t *testing.T, _, at string) {
			if err := syscall.Mkfifo(at, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		// A directory opens read-only like a file, and its attributes --
		// a default ACL among them -- are the account's to choose.
		{"directory", func(t *testing.T, _, at string) {
			if err := os.Mkdir(at, 0o755); err != nil {
				t.Fatal(err)
			}
			plantAttributes(t, at)
		}},
	} {
		t.Run(swap.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "elsewhere")
			if err := os.WriteFile(source, []byte("x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			plantAttributes(t, source)
			path := filepath.Join(dir, "conf")
			swap.make(t, source, path)
			tmpPath := filepath.Join(dir, "tmp")
			tmp, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer tmp.Close()
			done := make(chan error, 1)
			go func() { done <- keepExtendedAttributes(tmp, path, 0o600) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("a swapped name is nothing to keep, not an error: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("keepExtendedAttributes is still waiting on the swapped name")
			}
			untouched(t, tmpPath, 0o600)
		})
	}
}
