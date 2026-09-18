//go:build unix

package exec

import (
	"fmt"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"syscall"
)

// darwinMaxGroups is macOS's NGROUPS_MAX.
//
// `setgroups(2)` there refuses a list longer than this with EINVAL,
// however many groups an account really has -- macOS resolves the rest
// through Open Directory rather than carrying them in the process
// credential. The other platforms this build runs on allow far more,
// so the cap is macOS's alone and is applied there alone.
const darwinMaxGroups = 16

// capGroups trims a supplementary group list to what the platform will
// accept.
//
// A list the kernel refuses does not fail as a group error. It fails in
// `fork/exec` with "invalid argument", naming the *binary* -- so the
// report is `fork/exec /usr/bin/defaults: invalid argument` about a
// program that is present, executable and entirely innocent, and
// nothing in it says the word "group".
//
// Which is why this was invisible until a machine ran it. A personal
// Mac's account is in a handful of groups; a build account is in
// dozens. The module had been driven under `sudo` on a real Mac and
// passed, because that Mac's account fitted.
func capGroups(goos string, groups []uint32) []uint32 {
	if goos != "darwin" || len(groups) <= darwinMaxGroups {
		return groups
	}
	// The first entries are kept rather than a chosen subset: the
	// primary group leads the list `user.GroupIds` returns, and picking
	// among the rest would be this build deciding which of an account's
	// memberships matter. Dropping is the safe direction -- a child gets
	// fewer privileges than the account has, never more -- but it is a
	// real difference from the account's own login session, which is why
	// it is named here rather than done quietly.
	return groups[:darwinMaxGroups]
}

// applyCredential switches a child process to another account.
//
// setuid and setgid with the target's supplementary group set, rather
// than `su -c`: `su` starts a shell, reads that account's profile, and
// changes the environment out from under the command. SPEC section 15.2.
//
// "the target's group set" is the whole set everywhere but macOS, where
// the kernel will not take more than `darwinMaxGroups` of them; see
// capGroups.
func applyCredential(c *exec.Cmd, cmd Command) error {
	if cmd.RunAs == "" && cmd.Umask == "" {
		return nil
	}
	attr := c.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
		c.SysProcAttr = attr
	}

	if cmd.RunAs != "" {
		u, err := user.Lookup(cmd.RunAs)
		if err != nil {
			return fmt.Errorf("runas %q: %w", cmd.RunAs, err)
		}
		uid, err := strconv.ParseUint(u.Uid, 10, 32)
		if err != nil {
			return fmt.Errorf("runas %q: uid %q: %w", cmd.RunAs, u.Uid, err)
		}
		gid, err := strconv.ParseUint(u.Gid, 10, 32)
		if err != nil {
			return fmt.Errorf("runas %q: gid %q: %w", cmd.RunAs, u.Gid, err)
		}

		groupIDs, err := u.GroupIds()
		if err != nil {
			return fmt.Errorf("runas %q: supplementary groups: %w", cmd.RunAs, err)
		}
		groups := make([]uint32, 0, len(groupIDs))
		for _, g := range groupIDs {
			n, err := strconv.ParseUint(g, 10, 32)
			if err != nil {
				continue
			}
			groups = append(groups, uint32(n))
		}

		attr.Credential = &syscall.Credential{
			Uid:    uint32(uid),
			Gid:    uint32(gid),
			Groups: capGroups(runtime.GOOS, groups),
		}
		// The child gets the target account's home and user, so that a
		// command which reads either behaves as that account.
		c.Env = append(c.Env, "HOME="+u.HomeDir, "USER="+u.Username, "LOGNAME="+u.Username)
	}
	return nil
}
