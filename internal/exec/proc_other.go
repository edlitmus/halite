//go:build !unix

package exec

import "os/exec"

// setProcessGroup is a no-op off unix. Windows has no process-group signal
// of this shape; the WaitDelay set in Run still bounds a hung child there.
func setProcessGroup(c *exec.Cmd) {}
