//go:build !unix

package keystore

// inheritOwner does nothing where there is no uid to inherit. Windows
// files take their permissions from the directory's ACL already, which
// is the behaviour this restores elsewhere.
func inheritOwner(path, dir string) error { return nil }
