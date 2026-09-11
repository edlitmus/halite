//go:build unix

package rendersandbox

import (
	"fmt"
	"os/user"
	"strconv"
)

// renderAccount resolves the configured account, or reports zeroes when
// none is configured.
func renderAccount(cfg Config) (uint32, uint32, error) {
	if cfg.User == "" {
		return 0, 0, nil
	}
	return lookupIDs(cfg.User, cfg.Group)
}

// lookupIDs resolves the render account.
//
// A misspelled account is an error at the first render rather than a
// silent fallback to root. That is the whole value of the setting: an
// operator who wrote `render_sandbox_user: halite-rendr` and got a
// privileged render anyway would have a control that reports itself as
// applied and is not.
func lookupIDs(name, group string) (uint32, uint32, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, fmt.Errorf("the render sandbox account %q: %w", name, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	gidText := u.Gid
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return 0, 0, fmt.Errorf("the render sandbox group %q: %w", group, err)
		}
		gidText = g.Gid
	}
	gid, err := strconv.ParseUint(gidText, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	return uint32(uid), uint32(gid), nil
}
