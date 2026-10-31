//go:build !linux

package atomicfile

import "os"

// keepExtendedAttributes is Linux's: SELinux labels and POSIX ACLs live in a
// file's extended attributes there, and no other platform here keeps them
// that way.
func keepExtendedAttributes(tmp, path string, mode os.FileMode) error { return nil }
