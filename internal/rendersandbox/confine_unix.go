//go:build unix && !linux

package rendersandbox

import (
	"os"
	"os/exec"
	"syscall"
)

// applyConfinement is the other-unix half: an unprivileged account and a
// process group, which is everything this platform can do here with the
// standard library alone.
//
// There is no portable network namespace outside Linux. FreeBSD has
// jails and OpenBSD has pledge, and both are the same kind of answer as
// SPEC 24.3's list for extensions: real, and not reachable without
// either cgo or a dependency, which SPEC section 4.2 rules out. So the
// child on these platforms gets a process boundary and a dropped
// identity, and `Describe` says exactly that rather than implying more.
func applyConfinement(cmd *exec.Cmd, cfg Config) error {
	attr := &syscall.SysProcAttr{Setpgid: true}
	// Resolved whether or not it can be applied: see the note in
	// confine_linux.go. A name nobody can look up is a mistake in the
	// configuration on any machine.
	uid, gid, err := renderAccount(cfg)
	if err != nil {
		return err
	}
	if cfg.User != "" && os.Geteuid() == 0 {
		attr.Credential = &syscall.Credential{Uid: uid, Gid: gid}
	}
	cmd.SysProcAttr = attr
	return nil
}

func networkEnforcement() string {
	return "network: NOT denied; this platform has no namespace this build can use, " +
		"so the child could open a socket if the code in it tried to"
}

func platformNotes() []string {
	return []string{
		"no syscall filter: pledge and Capsicum are not reachable without cgo, which SPEC 4.2 rules out",
		"no filesystem restriction: the child reads what its account can read",
	}
}

// canDropPrivilege reports whether this process can hand the child a
// different identity.
func canDropPrivilege() bool { return os.Geteuid() == 0 }
