//go:build windows

package doctor

import "golang.org/x/sys/windows"

// Free reports the bytes available to the calling account.
//
// GetDiskFreeSpaceEx's first output is the free space available to the
// caller, which is what a quota makes different from the volume's own
// free space. That is the number that decides whether the next write
// succeeds.
func Free(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return avail, nil
}
