//go:build !unix

package builtin

import (
	"fmt"
	"os"
	"path/filepath"
)

// resolveTrusted, where there is no Unix ownership to judge a link by,
// resolves it as before and does not check who made it. On Windows a
// symbolic link needs SeCreateSymbolicLinkPrivilege, which an ordinary
// account does not hold unless Developer Mode is on; that is a narrower
// hole than the Unix one, but it is not checked, and nothing here has
// been run against it. The Unix rule is followlink_unix.go's.
func resolveTrusted(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		to, _ := os.Readlink(path)
		return "", fmt.Errorf("%s is a symbolic link to %s, which does not resolve: %w", path, to, err)
	}
	return real, nil
}
