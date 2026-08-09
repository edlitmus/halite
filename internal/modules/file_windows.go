//go:build windows

package modules

// Ownership management is not implemented on Windows.
func statOwner(path string) (uid, gid int, ok bool) { return 0, 0, false }

func chown(path string, uid, gid int) error { return nil }
