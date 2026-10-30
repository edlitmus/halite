//go:build linux

package builtin

import (
	"errors"
	"io/fs"
	"syscall"
)

// selinuxLabelAttrName is the extended attribute that holds a file's SELinux
// context. It is a variable only so a test can name another attribute.
var selinuxLabelAttrName = "security.selinux"

// keepSELinuxLabel gives tmp the SELinux context that path has. A config
// file is replaced through a temporary file and a rename, and the new file
// takes the context the directory's policy gives a *new* file, not the old
// file's: on Rocky 9.8 enforcing, /etc/hosts went from net_conf_t to etc_t
// and `restorecon -n` offered to put it back (issue 321, section 5;
// DIVERGENCE 5.484). Salt writes in place and never lost it.
//
// Copying the attribute, rather than calling restorecon, keeps whatever
// context the file had, including one an administrator set on purpose. A
// host with no SELinux, or a filesystem with no such attribute, has nothing
// to copy and is not an error; so is a refusal to set it, which is what a
// confined process gets and which must not stop the write.
func keepSELinuxLabel(tmp, path string) error {
	buf := make([]byte, 256)
	n, err := syscall.Getxattr(path, selinuxLabelAttrName, buf)
	if err == syscall.ERANGE {
		buf = make([]byte, 4096)
		n, err = syscall.Getxattr(path, selinuxLabelAttrName, buf)
	}
	if err != nil {
		if errors.Is(err, syscall.ENODATA) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS) {
			return nil
		}
		return err
	}
	if err := syscall.Setxattr(tmp, selinuxLabelAttrName, buf[:n], 0); err != nil &&
		!errors.Is(err, fs.ErrPermission) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}
