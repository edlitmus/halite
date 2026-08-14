//go:build !windows

package modules

import (
	"os"
	"syscall"
)

// statOwner returns the numeric uid/gid of path, ok=false if unavailable.
func statOwner(path string) (uid, gid int, ok bool) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	if s, okc := st.Sys().(*syscall.Stat_t); okc {
		return int(s.Uid), int(s.Gid), true
	}
	return 0, 0, false
}

func chown(path string, uid, gid int) error { return os.Chown(path, uid, gid) }

// lchown sets the ownership of a symlink itself rather than its target.
func lchown(path string, uid, gid int) error { return os.Lchown(path, uid, gid) }
