//go:build !windows

package builtin

import "fmt"

// win_dacl off Windows.
//
// The signatures declare `platforms: windows`, so a call is refused
// before it reaches any of this and an operator is told which platform
// the function runs on. These exist so the module registers everywhere —
// making `sys.doc win_dacl.grant` answerable from a hub on Linux, and
// keeping "not written yet" and "you have mistyped it" different
// messages.

func notWindows(function string) error {
	return fmt.Errorf("win_dacl.%s manages a Windows access control list, "+
		"and this node is not Windows", function)
}

func winDACLGetOwner(path string) (string, error) { return "", notWindows("get_owner") }

func winDACLSetOwner(path, owner string) error { return notWindows("set_owner") }

func winDACLEntries(path string) (any, error) { return nil, notWindows("get_permissions") }

func winDACLHasPermission(path, trustee, permission string, exact bool) (bool, error) {
	return false, notWindows("has_permission")
}

func winDACLGrant(path, trustee, permission, scope string, deny bool) error {
	return notWindows("grant")
}

func winDACLRevoke(path, trustee string) error { return notWindows("revoke") }

func winDACLInherits(path string) (bool, error) { return false, notWindows("get_inheritance") }

func winDACLSetInheritance(path string, on, clear bool) error {
	return notWindows("set_inheritance")
}

func winDACLEntryFor(path, trustee string) (string, error) { return "", notWindows("present") }
