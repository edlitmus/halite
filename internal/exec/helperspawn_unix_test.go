//go:build !windows

package exec

import (
	"os"
	"os/exec"
	"syscall"
)

// startDetached starts a grandchild in its own process group, so that
// killing the helper's group does not take it too. That is what makes
// the tree-kill test a test: the grandchild has to be reachable only by
// killing the tree deliberately.
func startDetached(argv []string) error {
	c := exec.Command(argv[0], argv[1:]...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	return c.Start()
}
