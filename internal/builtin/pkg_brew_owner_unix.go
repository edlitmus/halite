//go:build unix

package builtin

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// brewBinaryOwner is the account that owns the brew program at path.
//
// os.Stat rather than Lstat, so that a brew reached through a symlink is
// answered by the program rather than the link, as Salt's mac_brew_pkg
// does (`file.get_user` on the brew binary follows the link). On the one
// layout this project supports -- Apple silicon, /opt/homebrew/bin/brew
// -- it is a regular file and the two agree. Intel Macs, where
// /usr/local/bin/brew is a link into /usr/local/Homebrew, are not
// supported (decided 2026-10-01), so that branch has never been run.
func brewBinaryOwner(path string) (*user.User, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("%s: no owner in its file information", path)
	}
	uid := strconv.FormatUint(uint64(st.Uid), 10)
	u, err := user.LookupId(uid)
	if err != nil {
		return nil, fmt.Errorf("%s is owned by uid %s, which names no account: %w", path, uid, err)
	}
	return u, nil
}
