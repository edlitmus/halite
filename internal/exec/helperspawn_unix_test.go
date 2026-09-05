//go:build !windows

package exec

import (
	"os"
	"os/exec"
)

// startDetached starts a grandchild, which on a unix stays in the
// helper's process group.
//
// It set Setpgid once, so that the grandchild left the group its parent
// was in. That made the tree-kill test unpassable here rather than
// harder: a process group is the only handle a unix gives on a tree, and
// a descendant that leaves the group is out of its reach by
// construction — confineTree's own comment says so, and names WaitDelay
// as the backstop for exactly that case. The assertion held on Windows,
// where a job object catches a child that starts a new process group,
// and Windows was the only platform it had been run on: the builtin
// package did not compile on Linux at the time, so nothing ran there.
//
// So the two platforms are tested for what they each actually promise.
// Both kill a forked grandchild. Only Windows kills one that has gone
// out of its way to escape, and that is a real difference between a job
// object and a process group rather than a gap in this build.
func startDetached(argv []string) error {
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	return c.Start()
}
