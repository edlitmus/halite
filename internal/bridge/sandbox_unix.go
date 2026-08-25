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
func (s *Sandbox) applyPlatform(cmd *exec.Cmd) error {
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
			return err
		}
		attr.Credential = &syscall.Credential{Uid: uid, Gid: gid}
	}
	cmd.SysProcAttr = attr
	cmd.Env = append(cmd.Env, s.limitEnvironment()...)
	return nil
}

// limitEnvironment carries the limits to a child that knows how to
// apply them to itself.
func (s *Sandbox) limitEnvironment() []string {
	var out []string
	if s.MemoryBytes > 0 {
		out = append(out, "HALITE_EXT_RLIMIT_AS="+strconv.FormatUint(s.MemoryBytes, 10))
	}
	if s.CPUSeconds > 0 {
		out = append(out, "HALITE_EXT_RLIMIT_CPU="+strconv.FormatUint(s.CPUSeconds, 10))
	}
	if s.OpenFiles > 0 {
		out = append(out, "HALITE_EXT_RLIMIT_NOFILE="+strconv.FormatUint(s.OpenFiles, 10))
	}
	if s.Processes > 0 {
		out = append(out, "HALITE_EXT_RLIMIT_NPROC="+strconv.FormatUint(s.Processes, 10))
	}
	if !s.Network {
		out = append(out, "HALITE_EXT_NETWORK=deny")
	}
	return out
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

func limitsSupported() bool { return true }

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
