//go:build !windows

package fileperm

import (
	"fmt"
	"os"
)

// Apply sets the mode. On unix that is the whole of it.
func Apply(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

// ApplyFile is Apply on an open file, so that a caller can set the
// permissions before anything else can see the path.
func ApplyFile(f *os.File, mode os.FileMode) error {
	return f.Chmod(mode)
}

// Others names what can read the file beyond its owner. The mode is the
// answer, so the name is the mode.
func Others(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	perm := info.Mode().Perm()
	if perm&0o077 == 0 {
		return nil, nil
	}
	return []string{fmt.Sprintf("mode %o", perm)}, nil
}

// Advice is what to run to make the file private.
func Advice(path string) string { return fmt.Sprintf("chmod 600 %s", path) }
