//go:build unix

package keystore

import (
	"os"
	"syscall"
)

// geteuid and chown are variables so that the one thing here that needs
// root, handing ownership to someone else, can be exercised by a test that
// is not root. The root-only tests in this package skip everywhere a
// developer or the CI runner is an ordinary user, which was every run until
// this seam: the code that makes `keys accept` work as root had no test that
// ever ran, and the token store's directory was missed for that reason.
// DIVERGENCE 5.285.
var (
	geteuid = os.Geteuid
	chown   = os.Chown
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
	if geteuid() != 0 {
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
	if int(st.Uid) == geteuid() && int(st.Gid) == os.Getegid() {
		return nil
	}
	return chown(path, int(st.Uid), int(st.Gid))
}
