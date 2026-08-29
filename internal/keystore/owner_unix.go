//go:build unix

package keystore

import (
	"os"
	"syscall"
)

// inheritOwner gives a file the ownership of the directory holding it,
// when this process is root and the directory belongs to someone else.
//
// The operator runs `halite-hub keys accept` as root; the hub runs as an
// unprivileged service account. Without this the accepted record is
// written owned by root into a directory owned by that account, and the
// hub cannot read the record it was just told to honour — the node's
// next enrollment fails with `permission denied` on a file the operator
// can see perfectly well.
//
// Only downward: a process that is not root cannot change ownership, and
// a directory this process already owns needs nothing done.
func inheritOwner(path, dir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(st.Uid) == os.Geteuid() && int(st.Gid) == os.Getegid() {
		return nil
	}
	return os.Chown(path, int(st.Uid), int(st.Gid))
}
