//go:build !unix && !windows

package exec

import "os/exec"

// confineTree does nothing on a platform with neither process groups nor
// job objects. The WaitDelay set in Run still bounds a hung child there.
func confineTree(c *exec.Cmd) (afterStart func(), cleanup func()) {
	return func() {}, func() {}
}
