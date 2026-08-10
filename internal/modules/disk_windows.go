package modules

import "fmt"

// statfsUsage has no Windows implementation yet: disk.usage there needs
// GetDiskFreeSpaceEx, which is a syscall binding this package does not
// carry. Reporting the gap beats reporting wrong numbers.
func statfsUsage(path string) (map[string]any, error) {
	return nil, fmt.Errorf("disk usage is not implemented on Windows")
}

// DiskUsedPercent has no Windows implementation, for the same reason.
func DiskUsedPercent(path string) (int, error) {
	return 0, fmt.Errorf("disk usage is not implemented on Windows")
}
