//go:build linux

package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// keepExtendedAttributes gives the open temporary file the extended
// attributes that path has: its SELinux context, its POSIX ACLs, and whatever
// else is stored there. A path that is not a regular file, or is gone, has
// nothing to give.
//
// A config file is replaced through a temporary file and a rename, and a new
// inode starts with none of the old one's. The first loss recorded was the
// SELinux label: on Rocky 9.8 enforcing, /etc/hosts went from net_conf_t to
// etc_t and `restorecon -n` offered to put it back (issue 321, section 5;
// DIVERGENCE 5.484). The ACL (`system.posix_acl_access`) and user attributes
// went the same way and nothing named them (5.471). Salt writes in place, so
// it keeps all of them; this copies all of them rather than a list, because a
// list is the thing that was missing the ACL.
//
// Copying the SELinux attribute, rather than calling restorecon, keeps
// whatever context the file had, including one an administrator set on
// purpose. An ACL copied after the temporary file was given its mode is
// consistent with it: the old mode's group bits are the ACL mask, and the ACL
// attribute carries the mask itself.
//
// A filesystem with no extended attributes has nothing to copy and is not an
// error. Neither is a refusal to set one, which is what a confined or
// unprivileged process gets for `security.*` and `trusted.*`, and which must
// not stop the write; the attribute is then lost as it always was.
//
// **The caller's mode still wins.** Setting an ACL sets the file's group
// bits to the ACL's mask, so a caller that wrote a secret with 0600 over a
// file whose ACL mask was r-- would get a file whose named entries can read
// it. After an ACL is copied the mode is applied again: chmod on a file with
// an ACL rewrites the mask from the group bits, so the copied entries can
// only ever be narrowed by what the caller asked for, never widened past it.
//
// **Both files are reached through descriptors, never by name** (DIVERGENCE
// 5.566). The temporary file sits in the target's directory, and when that
// directory is an account's -- root writing `~deploy/.profile` -- the account
// owns the replaced file, so it chooses the ACL that is copied, and it can
// rename the temporary name away and put a link to any file there. This used
// to run after the close, with `setxattr` and `chmod` by name, both of which
// follow a symbolic link and neither of which can tell a hard link from the
// file it names: the ACL the account chose, and the caller's mode, landed on
// whatever it linked, as root. Against /etc/shadow that is an entry granting
// the account read and write. `fsetxattr` and `fchmod` on the descriptor
// change the file this package created and nothing else, whatever its name
// says by then. `syscall` exports `Setxattr` and not `Fsetxattr`, and the one
// vendored module that has it is not allowed here (internal/buildpolicy), so
// the three f-calls are made directly, with the system call numbers
// `syscall` defines for each architecture.
//
// The replaced file is opened with O_NOFOLLOW, so a name the account swapped
// for a symbolic link since the caller looked gives nothing, and O_NONBLOCK,
// so one swapped for a FIFO does not hang the run; what was opened must then
// be a regular file. That leaves a hard link. An account that can link a file
// into its directory -- one it owns, or any file on a host with
// fs.protected_hardlinks=0 -- has its attributes copied onto the new file.
// What that discloses is small: an ACL and an SELinux label are readable by
// anyone who can reach the file (`getfacl` and `ls -Z` need no permission on
// it), `trusted.*` is copied but readable again only with CAP_SYS_ADMIN, and
// what is left is the `user.*` attributes of a file the account could not
// read, on a file it may be able to. Nothing is written to the linked file.
// Opening needs read permission where listing by name did not, so a writer
// that may replace a file but not read it now copies nothing, rather than
// following a name to do it; a root writer is never refused.
func keepExtendedAttributes(tmp *os.File, path string, mode os.FileMode) error {
	old, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		// Not there, a symbolic link (ELOOP), or not ours to read: nothing
		// to keep, which is what a missing file always meant here.
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) || errors.Is(err, fs.ErrPermission) {
			return nil
		}
		return err
	}
	defer old.Close()
	if fi, err := old.Stat(); err != nil {
		return err
	} else if !fi.Mode().IsRegular() {
		return nil
	}
	names, err := listXattr(old)
	if err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS) {
			return nil
		}
		return err
	}
	copiedACL := false
	for _, name := range names {
		val, err := getXattr(old, name)
		if err != nil {
			// Gone between the list and the read, or unreadable to us.
			if errors.Is(err, syscall.ENODATA) || errors.Is(err, fs.ErrPermission) {
				continue
			}
			return err
		}
		if err := setXattr(tmp, name, val); err != nil &&
			!errors.Is(err, fs.ErrPermission) && !errors.Is(err, syscall.ENOTSUP) {
			return err
		}
		if strings.HasPrefix(name, "system.posix_acl_") {
			copiedACL = true
		}
	}
	if copiedACL {
		return tmp.Chmod(mode)
	}
	return nil
}

// fdCall runs f with the file's descriptor, kept open for the duration;
// SyscallConn rather than Fd, which would also switch the file to blocking
// mode.
func fdCall(f *os.File, call func(fd uintptr) (uintptr, syscall.Errno)) (int, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		n     uintptr
		errno syscall.Errno
	)
	if err := rc.Control(func(fd uintptr) { n, errno = call(fd) }); err != nil {
		return 0, err
	}
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

// bufPtr is the address of b's first byte, or nil for an empty b, which is
// how the xattr calls are asked for a size.
func bufPtr(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}

func flistxattr(f *os.File, buf []byte) (int, error) {
	return fdCall(f, func(fd uintptr) (uintptr, syscall.Errno) {
		n, _, e := syscall.Syscall(syscall.SYS_FLISTXATTR, fd, uintptr(bufPtr(buf)), uintptr(len(buf)))
		return n, e
	})
}

func fgetxattr(f *os.File, name string, buf []byte) (int, error) {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return 0, err
	}
	return fdCall(f, func(fd uintptr) (uintptr, syscall.Errno) {
		n, _, e := syscall.Syscall6(syscall.SYS_FGETXATTR, fd, uintptr(unsafe.Pointer(p)), uintptr(bufPtr(buf)), uintptr(len(buf)), 0, 0)
		return n, e
	})
}

// setXattr is fsetxattr(2) with no flags: create or replace.
func setXattr(f *os.File, name string, val []byte) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, err = fdCall(f, func(fd uintptr) (uintptr, syscall.Errno) {
		_, _, e := syscall.Syscall6(syscall.SYS_FSETXATTR, fd, uintptr(unsafe.Pointer(p)), uintptr(bufPtr(val)), uintptr(len(val)), 0, 0)
		return 0, e
	})
	return err
}

// listXattr is the names of f's extended attributes. The list is
// NUL-separated; its size is asked for first, and asked again if the file
// gained an attribute in between.
func listXattr(f *os.File) ([]string, error) {
	for {
		n, err := flistxattr(f, nil)
		if err != nil || n == 0 {
			return nil, err
		}
		buf := make([]byte, n)
		n, err = flistxattr(f, buf)
		if err == syscall.ERANGE {
			continue
		}
		if err != nil {
			return nil, err
		}
		return splitNul(buf[:n]), nil
	}
}

func splitNul(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == 0 {
			if i > start {
				out = append(out, string(b[start:i]))
			}
			start = i + 1
		}
	}
	return out
}

func getXattr(f *os.File, name string) ([]byte, error) {
	for {
		n, err := fgetxattr(f, name, nil)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, n)
		n, err = fgetxattr(f, name, buf)
		if err == syscall.ERANGE {
			continue
		}
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}
}
