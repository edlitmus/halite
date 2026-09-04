package fileperm

import (
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/winsec"
)

// Apply carries out what the mode asks for.
//
// The read-only attribute is set either way, because a caller that asked
// for a file with no write bits meant it. A mode that denies group and
// other means "no other account may read this", which on this platform
// is an access control list granting the owner, SYSTEM and
// Administrators and nobody else.
func Apply(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if mode&0o077 != 0 {
		return nil
	}
	// A directory takes the inheriting form, or the restriction stops
	// at the directory itself and a file written into it afterwards
	// picks up whatever the parent above would have given it.
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return winsec.RestrictDir(path)
	}
	return winsec.Restrict(path)
}

// ApplyFile is Apply on an open file, so that a caller can set the
// permissions before anything else can see the path.
func ApplyFile(f *os.File, mode os.FileMode) error {
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if mode&0o077 != 0 {
		return nil
	}
	return winsec.Restrict(f.Name())
}

// Others names the accounts that can reach the file beyond its owner,
// SYSTEM and Administrators.
func Others(path string) ([]string, error) { return winsec.Others(path) }

// Advice is what to run to make the file private, in the form an
// administrator on this platform would type.
func Advice(path string) string {
	return fmt.Sprintf(
		`icacls "%s" /inheritance:r /grant:r "%%USERNAME%%:F" SYSTEM:F Administrators:F`, path)
}
