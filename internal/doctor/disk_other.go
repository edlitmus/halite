//go:build !windows && !linux && !darwin && !freebsd && !dragonfly && !openbsd

package doctor

// Free is not reportable here.
//
// NetBSD's `syscall` declares `Statfs_t` as `[0]byte` and offers no
// `Statfs`; Solaris, illumos and AIX have statvfs, which it does not
// expose either. There is no answer to give without a dependency SPEC
// 4.2 has not agreed to, and saying so is what makes `DiskFree` report
// a skip with a reason rather than a pass on a disk it never looked at.
//
// Every one of those is SPEC 27.1's tier 3 — "compiles and is
// published" — and this file is why the tier still compiles. The
// alternative was found the hard way: the first version of the statfs
// file claimed every unix and broke the build for two of them. See
// DIVERGENCE 4.10.
func Free(path string) (uint64, error) {
	return 0, ErrNotReportable
}
