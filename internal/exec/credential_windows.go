package exec

import (
	"fmt"
	"os/exec"
	"os/user"
)

// applyCredential switches a child process to another account.
//
// It refuses on Windows, and the refusal is the point. There is no
// setuid here: a process runs as the account whose token it was given,
// and obtaining another account's token means either the account's
// password, through LogonUser, or the SeAssignPrimaryToken and
// SeIncreaseQuota privileges, through CreateProcessAsUser — neither of
// which the caller has supplied. Running the command as the service
// account instead, which is what an unimplemented no-op amounts to,
// would run it with more authority than was asked for, not less.
//
// The account is looked up first so that the two failures stay
// distinct. `runas: nosuchuser` is a mistake in the state and is
// reported as one on every platform; `runas: someone-real` is a
// platform gap, and telling an operator their account does not exist
// when it does sends them to the wrong place. This refusal named
// neither, and said "not supported on this platform yet" whatever it
// was handed.
func applyCredential(c *exec.Cmd, cmd Command) error {
	if cmd.RunAs == "" {
		return nil
	}
	if _, err := user.Lookup(cmd.RunAs); err != nil {
		return fmt.Errorf("runas %q: %w", cmd.RunAs, err)
	}
	return fmt.Errorf(
		"runas %q: running as another account on Windows needs that account's "+
			"credentials, which halite does not hold; run the node service as "+
			"that account, or use a scheduled task", cmd.RunAs)
}
