//go:build !unix

package exec

import (
	"fmt"
	"os/exec"
)

// applyCredential is not implemented off unix. Windows uses a restricted
// token rather than setuid, which arrives with the Windows module set in a
// later phase; refusing loudly is better than silently running as the
// service account.
func applyCredential(c *exec.Cmd, cmd Command) error {
	if cmd.RunAs != "" {
		return fmt.Errorf("runas is not supported on this platform yet")
	}
	return nil
}
