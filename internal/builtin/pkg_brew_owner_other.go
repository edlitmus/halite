//go:build !unix

package builtin

import (
	"fmt"
	"os/user"
)

// brewBinaryOwner has no answer off unix, where there is no Homebrew to
// own. The provider is only Available where `brew` is on the path, so
// this is reached only by a machine that has a program called brew and
// no uid to own it.
func brewBinaryOwner(path string) (*user.User, error) {
	return nil, fmt.Errorf("%s: finding the account that owns brew needs a unix file owner", path)
}
