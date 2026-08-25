//go:build !unix

package bridge

import "os/exec"

// applyPlatform does what this platform allows, which is the process
// boundary and nothing else.
//
// Windows has restricted tokens and job objects, which SPEC 24.3 names
// and this build does not have. Saying so is the point: an operator
// reading `sys.list_extensions` on Windows must not believe an
// extension is confined when it is not.
func (s *Sandbox) applyPlatform(cmd *exec.Cmd) error { return nil }

func limitsSupported() bool { return false }

func networkEnforcement() string { return "not granted, and not enforced on this platform" }

func sandboxPlatformNotes() []string {
	return []string{"no restricted token or job object: SPEC 24.3, not built"}
}
