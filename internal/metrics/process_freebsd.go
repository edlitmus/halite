//go:build freebsd && (amd64 || arm64)

package metrics

import (
	"encoding/binary"
	"os"
	"syscall"
	"unsafe"
)

// FreeBSD has no /proc by default, and golang.org/x/sys/unix has no
// kinfo_proc for it, so these are sysctl(3) calls made through syscall.
//
// The offsets are struct kinfo_proc's in sys/sys/user.h (releng/14.3),
// for LP64 -- amd64 and arm64, the two FreeBSD targets this project
// ships: ki_structsize at 0, ki_pid at 72, ki_rssize (segsz_t, an int64
// of pages) at 264. A wrong offset here would not fail; it would export
// some other field as the hub's memory. So every read checks the two
// fields it can check against something it already knows --
// ki_structsize against the length the kernel returned, ki_pid against
// os.Getpid -- and a mismatch is "cannot read", not a number. The
// FreeBSD CI leg holds the result to `ps -o rss=` (process_test.go).

const (
	ctlKern       = 1  // CTL_KERN
	kernProc      = 14 // KERN_PROC
	kernProcPID   = 1  // KERN_PROC_PID
	kernProcNFDS  = 43 // KERN_PROC_NFDS, FreeBSD 13.0 and later
	kinfoPIDOff   = 72
	kinfoRSSOff   = 256 // DELIBERATELY WRONG: ki_size, to prove CI runs this
	kinfoMinBytes = kinfoRSSOff + 8
)

// sysctl is sysctl(3) by MIB, returning at most size bytes.
func sysctl(mib []int32, size int) ([]byte, error) {
	buf := make([]byte, size)
	n := uintptr(size)
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)),
		0, 0)
	if errno != 0 {
		return nil, errno
	}
	return buf[:n], nil
}

func residentBytes() (float64, bool) {
	pid := os.Getpid()
	// kinfo_proc is 1088 bytes on LP64 (KINFO_PROC_SIZE); a larger
	// buffer costs nothing and lets a future, longer struct through to
	// the size check rather than failing with ENOMEM.
	raw, err := sysctl([]int32{ctlKern, kernProc, kernProcPID, int32(pid)}, 4096)
	if err != nil || len(raw) < kinfoMinBytes {
		return 0, false
	}
	le := binary.LittleEndian // amd64 and arm64 FreeBSD are both little-endian
	if int(int32(le.Uint32(raw[0:]))) != len(raw) ||
		int(int32(le.Uint32(raw[kinfoPIDOff:]))) != pid {
		return 0, false
	}
	pages := int64(le.Uint64(raw[kinfoRSSOff:]))
	if pages < 0 {
		return 0, false
	}
	return float64(pages) * float64(os.Getpagesize()), true
}

// openFDs is kern.proc.nfds, which counts the calling process's open
// descriptors and takes 0 for its one argument (kern_descrip.c,
// sysctl_kern_proc_nfds). /dev/fd would not do: without fdescfs mounted
// it lists only 0, 1 and 2.
func openFDs() (float64, bool) {
	raw, err := sysctl([]int32{ctlKern, kernProc, kernProcNFDS, 0}, 4)
	if err != nil || len(raw) != 4 {
		return 0, false
	}
	return float64(int32(binary.LittleEndian.Uint32(raw))), true
}

func maxFDs() (float64, bool) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0, false
	}
	return float64(lim.Cur), true
}
