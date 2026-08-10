//go:build !windows

package modules

import (
	"fmt"
	"syscall"
)

// DiskUsedPercent reports how full the filesystem holding path is, as a
// whole percentage. Beacons use it to watch for a filling disk.
func DiskUsedPercent(path string) (int, error) {
	usage, err := statfsUsage(path)
	if err != nil {
		return 0, err
	}
	total, _ := usage["total_bytes"].(uint64)
	used, _ := usage["used_bytes"].(uint64)
	if total == 0 {
		return 0, fmt.Errorf("%s reports no capacity", path)
	}
	return int(used * 100 / total), nil
}

// statfsUsage reports space for the filesystem holding path. The Statfs_t
// field widths differ between kernels, so every field is widened before
// arithmetic.
func statfsUsage(path string) (map[string]any, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return nil, fmt.Errorf("statfs %s: %w", path, err)
	}
	blockSize := uint64(st.Bsize)
	total := uint64(st.Blocks) * blockSize
	free := uint64(st.Bavail) * blockSize
	used := total - uint64(st.Bfree)*blockSize

	usage := map[string]any{
		"total_bytes": total,
		"used_bytes":  used,
		"free_bytes":  free,
		"total":       humanBytes(total),
		"used":        humanBytes(used),
		"free":        humanBytes(free),
	}
	if total > 0 {
		usage["capacity"] = fmt.Sprintf("%d%%", used*100/total)
	}
	return usage, nil
}
