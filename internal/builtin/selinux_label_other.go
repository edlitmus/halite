//go:build !linux

package builtin

// keepSELinuxLabel is Linux's: no other platform here has SELinux labels
// in a file's extended attributes.
func keepSELinuxLabel(tmp, path string) error { return nil }
