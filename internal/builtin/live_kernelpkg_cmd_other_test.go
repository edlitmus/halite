//go:build !unix

package builtin

import osexec "os/exec"

// stopTreeOnCancel leaves the default cancel, which kills the command
// alone. The kernelpkg live tests run package commands only on Linux and
// in the EL containers; elsewhere they skip before reaching one.
func stopTreeOnCancel(*osexec.Cmd) {}
