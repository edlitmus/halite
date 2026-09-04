//go:build !unix && !windows

package exec

import (
	"fmt"
	"os/exec"
)

// applyCredential is not implemented on this platform.
func applyCredential(c *exec.Cmd, cmd Command) error {
	if cmd.RunAs != "" {
		return fmt.Errorf("runas %q: switching accounts is not implemented on this platform", cmd.RunAs)
	}
	return nil
}
