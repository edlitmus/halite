//go:build !unix && !windows

package bridge

import "os/exec"

// applyPlatform does what this platform allows, which is the process
// boundary and nothing else.
func (s *Sandbox) applyPlatform(cmd *exec.Cmd) (func() error, func(), error) {
	return func() error { return nil }, func() {}, nil
}

func limitsAvailable() limitSupport { return limitSupport{} }

func networkEnforcement() string { return "not granted, and not enforced on this platform" }

func sandboxPlatformNotes() []string {
	return []string{"no confinement mechanism on this platform"}
}
