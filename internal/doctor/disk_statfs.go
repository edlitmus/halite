//go:build linux || darwin || freebsd || dragonfly

package doctor

import "syscall"

// Free reports the bytes available to an unprivileged writer.
//
// `Bavail` rather than `Bfree`: the difference is the reserve a
// filesystem keeps for root, and a hub running as the halite account
// cannot use it. Reporting free space this process cannot have is how a
// check passes on a disk that is full for the purpose it is checked
// for.
//
// The field types are not the same on any two of these platforms —
// Bsize is int64 on Linux, uint64 on FreeBSD and uint32 on macOS, and
// Bavail flips between signed and unsigned alongside it. That is
// exactly the shape that stopped the tree compiling for macOS once and
// for OpenBSD again (DIVERGENCE 4.4a, 4.10), so the arithmetic takes
// its types from the compiler rather than declaring them.
//
// OpenBSD spells the same fields `F_bavail` and `F_bsize` and has its
// own file; NetBSD's `syscall` declares `Statfs_t` as `[0]byte` and no
// `Statfs` at all, so it falls to disk_other.go with the platforms that
// have statvfs instead.
func Free(path string) (uint64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, err
	}
	return blocks(fs.Bavail, fs.Bsize), nil
}
