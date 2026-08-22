package signature

import (
	"fmt"
	"runtime"
	"strings"
)

// "Platforms restricts the function; empty means every platform" is what
// the field's own documentation says, and nothing restricted anything.
// A `sysrc` call on Linux reached the module, which looked for a binary
// that is not there and reported that instead — a true statement about
// the wrong thing, since no Linux ever has one.
//
// Privileges is the same shape with a different answer. Every function
// that declares one is a mutating function that declares `root`, so
// refusing up front would be correct and would also refuse a `--test`
// run, which is the run an operator makes precisely because they are not
// ready to be root. So the declaration enriches a failure instead of
// preventing an attempt.

// PlatformError is returned when a function cannot run here at all.
type PlatformError struct {
	Function  string
	Platforms []string
	Here      string
}

func (e *PlatformError) Error() string {
	return fmt.Sprintf("%s runs on %s, and this node is %s",
		e.Function, strings.Join(e.Platforms, ", "), e.Here)
}

// CheckPlatform reports whether a function can run on this platform.
func (s Signature) CheckPlatform() error {
	if len(s.Platforms) == 0 {
		return nil
	}
	for _, p := range s.Platforms {
		if p == runtime.GOOS {
			return nil
		}
	}
	return &PlatformError{Function: s.Name(), Platforms: s.Platforms, Here: runtime.GOOS}
}

// PrivilegeNote explains a failure that a declared privilege is likely to
// account for, or returns "" when it is not.
//
// It is appended to a failure rather than checked before one, because
// the same function under `--test` changes nothing and an operator runs
// `--test` precisely when they are not ready to be root.
func (s Signature) PrivilegeNote(euid int) string {
	if len(s.Privileges) == 0 || euid == 0 {
		return ""
	}
	for _, p := range s.Privileges {
		if p == "root" {
			return fmt.Sprintf(" (%s declares that it needs root, and this process is not root)", s.Name())
		}
	}
	return fmt.Sprintf(" (%s declares that it needs %s)", s.Name(), strings.Join(s.Privileges, ", "))
}
