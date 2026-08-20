//go:build unix

package exec

import (
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

// applyCredential switches a child process to another account.
//
// setuid and setgid with the target's full supplementary group set, rather
// than `su -c`: `su` starts a shell, reads that account's profile, and
// changes the environment out from under the command. SPEC section 15.2.
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
			Groups: groups,
		}
		// The child gets the target account's home and user, so that a
		// command which reads either behaves as that account.
		c.Env = append(c.Env, "HOME="+u.HomeDir, "USER="+u.Username, "LOGNAME="+u.Username)
	}
	return nil
}
