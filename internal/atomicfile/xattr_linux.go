//go:build linux

package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

// keepExtendedAttributes gives tmp the extended attributes that path has: its
// SELinux context, its POSIX ACLs, and whatever else is stored there.
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
func keepExtendedAttributes(tmp, path string, mode os.FileMode) error {
	names, err := listXattr(path)
	if err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS) {
			return nil
		}
		return err
	}
	copiedACL := false
	for _, name := range names {
		val, err := getXattr(path, name)
		if err != nil {
			// Gone between the list and the read, or unreadable to us.
			if errors.Is(err, syscall.ENODATA) || errors.Is(err, fs.ErrPermission) {
				continue
			}
			return err
		}
		if err := syscall.Setxattr(tmp, name, val, 0); err != nil &&
			!errors.Is(err, fs.ErrPermission) && !errors.Is(err, syscall.ENOTSUP) {
			return err
		}
		if strings.HasPrefix(name, "system.posix_acl_") {
			copiedACL = true
		}
	}
	if copiedACL {
		return os.Chmod(tmp, mode)
	}
	return nil
}

// listXattr is the names of path's extended attributes. The list is
// NUL-separated; its size is asked for first, and asked again if the file
// gained an attribute in between.
func listXattr(path string) ([]string, error) {
	for {
		n, err := syscall.Listxattr(path, nil)
		if err != nil || n == 0 {
			return nil, err
		}
		buf := make([]byte, n)
		n, err = syscall.Listxattr(path, buf)
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

func getXattr(path, name string) ([]byte, error) {
	for {
		n, err := syscall.Getxattr(path, name, nil)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, n)
		n, err = syscall.Getxattr(path, name, buf)
		if err == syscall.ERANGE {
			continue
		}
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}
}
