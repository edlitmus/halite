package exec

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// startDetached starts a grandchild in a new process group, which on
// Windows still leaves it in the parent's job object — which is exactly
// the point: the tree kill has to reach it, and nothing else will.
func startDetached(argv []string) error {
	c := exec.Command(argv[0], argv[1:]...)
	c.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	return c.Start()
}
