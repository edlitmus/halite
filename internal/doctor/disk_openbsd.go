//go:build openbsd

package doctor

import "syscall"

// Free reports the bytes available to an unprivileged writer.
//
// OpenBSD's `Statfs_t` keeps the struct's own `f_` prefix, so the
// fields are `F_bavail` and `F_bsize` where every other BSD has
// `Bavail` and `Bsize`. One letter and a capital, and it is why this
// platform has a file rather than a build tag alongside the others —
// found by compiling for it, which nothing did until SPEC 27.1's tier 3
// went into `build-all`.
func Free(path string) (uint64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, err
	}
	return blocks(fs.F_bavail, fs.F_bsize), nil
}
