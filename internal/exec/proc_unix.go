//go:build unix

package exec

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the child in its own process group and makes the
// context's cancel signal that whole group, not just the direct child.
//
// A `cmd.run` with a Timeout that fired used to wait out the runaway
// anyway: os/exec kills only the process it started, so the shell died
// while the program it had forked kept running — and kept the write end of
// the stdout pipe open, so Wait blocked on the copy goroutine until the
// program exited on its own. Killing the group takes the shell, the
// program, and any grandchild down together, which is what a timeout is
// for. WaitDelay in Run is the backstop for a grandchild that put itself
// in a new group.
func setProcessGroup(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.Setpgid = true
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		// A negative pid is the process group. SIGKILL rather than a
		// catchable signal because this is the timeout path: the deadline
		// was the grace period.
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		return c.Process.Kill()
	}
}
