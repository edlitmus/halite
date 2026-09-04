//go:build unix

package bridge

import (
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

// applyPlatform sets the credentials and the process group.
//
// The resource limits of SPEC 24.3 are set by the child in `Confine`,
// not here: `setrlimit` applies to the calling process, and calling it
// in the host would bound the agent rather than the extension. Go's
// `SysProcAttr` has no rlimit field, so the limits are carried to the
// child in its environment and applied by it — which works for an
// extension built with this package and does nothing for one that is
// not, and `Describe` says so.
//
// Nothing here needs the child to exist, so both hooks are empty.
func (s *Sandbox) applyPlatform(cmd *exec.Cmd) (func() error, func(), error) {
	nothing := func() {}
	noHook := func() error { return nil }

	attr := &syscall.SysProcAttr{
		// Its own process group, so the kill that ends a timeout ends
		// whatever the extension started as well. An extension that
		// forks and leaves the child running is the shape of leak this
		// prevents.
		Setpgid: true,
	}
	if s.User != "" {
		uid, gid, err := lookupIDs(s.User, s.Group)
		if err != nil {
			return noHook, nothing, err
		}
		attr.Credential = &syscall.Credential{Uid: uid, Gid: gid}
	}
	cmd.SysProcAttr = attr
	return noHook, nothing, nil
}

func lookupIDs(name, group string) (uint32, uint32, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, fmt.Errorf("the extension account %q: %w", name, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	gidText := u.Gid
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return 0, 0, fmt.Errorf("the extension group %q: %w", group, err)
		}
		gidText = g.Gid
	}
	gid, err := strconv.ParseUint(gidText, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	return uint32(uid), uint32(gid), nil
}

// limitsAvailable: setrlimit covers all four, and the child applies
// them to itself.
//
// RLIMIT_AS bounds virtual address space, which a garbage-collected
// runtime reserves far more of than it commits — so the warning belongs
// beside the unbounded default here and nowhere else.
func limitsAvailable() limitSupport {
	return limitSupport{
		Memory: true, CPU: true, OpenFiles: true, Processes: true,
		MemoryLabel:     "address space",
		MemoryUnbounded: "address space unbounded (RLIMIT_AS kills a garbage-collected runtime)",
	}
}

func networkEnforcement() string {
	// Honest rather than reassuring. Denying the network to a child
	// process needs a namespace on Linux or a filter elsewhere, and
	// this build does neither: what it does is decline to say the
	// extension may use it, which an extension built with this package
	// honours and an arbitrary one does not.
	return "not granted; enforced only by an extension that honours the declaration"
}

func sandboxPlatformNotes() []string {
	return []string{
		"no syscall filter: seccomp-bpf and pledge are SPEC 24.3 and are not built",
		"no filesystem restriction: Landlock and unveil are SPEC 24.3 and are not built",
	}
}
