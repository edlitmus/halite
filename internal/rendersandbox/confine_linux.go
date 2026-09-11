//go:build linux

package rendersandbox

import (
	"os"
	"os/exec"
	"syscall"
)

// applyConfinement is the Linux half: an unprivileged account and a
// network namespace of the child's own.
//
// The namespace is what makes "with no network" of SPEC 25.4 a fact
// rather than a hope. A process in a fresh network namespace has one
// interface, loopback, and it is down: there is nothing to connect to
// and nothing to listen on, enforced by the kernel rather than by the
// child agreeing not to. It needs CAP_SYS_ADMIN, so it is applied only
// when this process is root, and `Describe` reports which of the two
// cases the operator is in.
//
// The order matters and the standard library gets it right: unshare runs
// before the credentials are dropped, so the namespace is created while
// there is still privilege to create it.
func applyConfinement(cmd *exec.Cmd, cfg Config) error {
	attr := &syscall.SysProcAttr{
		// Its own process group, so killing a child on a timeout kills
		// anything it started with it.
		Setpgid: true,
	}
	// The account is resolved whether or not it can be applied, so that
	// a misspelled one is an error on every machine rather than only on
	// the machines that could have used it. A lab run found the other
	// way round: `render_sandbox_user: no-such-account` on a node that
	// was not root rendered happily, which is a setting that reports
	// itself as configured and does nothing.
	uid, gid, err := renderAccount(cfg)
	if err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		attr.Unshareflags = syscall.CLONE_NEWNET
		if cfg.User != "" {
			attr.Credential = &syscall.Credential{Uid: uid, Gid: gid}
		}
	}
	cmd.SysProcAttr = attr
	return nil
}

func networkEnforcement() string {
	if os.Geteuid() == 0 {
		return "network: denied by the kernel; the child runs in a network namespace of its own, with loopback down"
	}
	return "network: NOT denied; a network namespace needs CAP_SYS_ADMIN and this process is not root"
}

func platformNotes() []string {
	return []string{
		"no syscall filter: the seccomp allowlist SPEC 25.4 asks for on the privileged parent is not built",
		"no filesystem restriction: the child reads what its account can read, which is not yet narrowed to the cached tree",
	}
}

// canDropPrivilege reports whether this process can hand the child a
// different identity, which on a unix means being root.
func canDropPrivilege() bool { return os.Geteuid() == 0 }
