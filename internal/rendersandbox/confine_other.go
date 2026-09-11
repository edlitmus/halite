//go:build !unix

package rendersandbox

import "os/exec"

// applyConfinement does nothing here.
//
// Windows has restricted tokens and job objects, and neither is
// reachable from the standard library in the way the credential fields
// on a unix SysProcAttr are. What the sandbox gives on this platform is
// the process boundary: the parser and the template engine run in a
// process that can crash, hang or be killed without taking the node with
// it, and nothing else is claimed.
func applyConfinement(cmd *exec.Cmd, cfg Config) error { return nil }

func networkEnforcement() string {
	return "network: NOT denied; this platform has no mechanism this build uses"
}

func platformNotes() []string {
	return []string{
		"identity: not dropped; a restricted token is the Windows answer and is not built",
		"the process boundary is the whole of the confinement here",
	}
}

// canDropPrivilege is false here: the credential fields a unix
// SysProcAttr carries have no counterpart this build uses on Windows.
func canDropPrivilege() bool { return false }

// renderAccount has nothing to resolve on a platform that cannot use it.
func renderAccount(cfg Config) (uint32, uint32, error) { return 0, 0, nil }
