package builtin

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/winsec"
)

// The Windows half of win_dacl. internal/winsec holds the security
// descriptor work; this is the translation between it and a module's
// arguments and return values.

func winDACLGetOwner(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("win_dacl.get_owner needs a path")
	}
	return winsec.Owner(path)
}

func winDACLSetOwner(path, owner string) error {
	if path == "" || owner == "" {
		return fmt.Errorf("win_dacl.set_owner needs a path and an owner")
	}
	return winsec.SetOwner(path, owner)
}

// winDACLEntries returns the list in the shape a template iterates.
func winDACLEntries(path string) (any, error) {
	if path == "" {
		return nil, fmt.Errorf("win_dacl.get_permissions needs a path")
	}
	entries, err := winsec.Entries(path)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, value.MapOf(
			"trustee", e.Trustee,
			"kind", e.Kind,
			"permission", e.Permission,
			"mask", int64(e.Mask),
			"applies_to", e.AppliesTo,
			"inherited", e.Inherited,
		))
	}
	return out, nil
}

// winDACLHasPermission reports whether a trustee is granted a level.
//
// "At least" by default, because that is the question an `onlyif` asks:
// an account with full_control does have read. `exact` is there for the
// other question, which a state comparing against a declaration asks.
//
// A deny entry never counts as having a permission, whatever its mask:
// Windows evaluates deny first, so an account with a deny entry does not
// have the access the mask names.
func winDACLHasPermission(path, trustee, permission string, exact bool) (bool, error) {
	if path == "" || trustee == "" {
		return false, fmt.Errorf("win_dacl.has_permission needs a path and a trustee")
	}
	want, err := winsec.PermissionMask(permission)
	if err != nil {
		return false, err
	}
	entries, err := winsec.Entries(path)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !sameAccount(e.Trustee, trustee) || e.Kind != "allow" {
			continue
		}
		if exact {
			if e.Mask == want {
				return true, nil
			}
			continue
		}
		if e.Mask&want == want {
			return true, nil
		}
	}
	return false, nil
}

func winDACLGrant(path, trustee, permission, scope string, deny bool) error {
	if path == "" || trustee == "" {
		return fmt.Errorf("win_dacl.grant needs a path and a trustee")
	}
	return winsec.Grant(path, trustee, permission, scope, deny)
}

func winDACLRevoke(path, trustee string) error {
	if path == "" || trustee == "" {
		return fmt.Errorf("win_dacl.revoke needs a path and a trustee")
	}
	return winsec.Revoke(path, trustee)
}

func winDACLInherits(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("win_dacl.get_inheritance needs a path")
	}
	return winsec.Inherits(path)
}

func winDACLSetInheritance(path string, on, clear bool) error {
	if path == "" {
		return fmt.Errorf("win_dacl.set_inheritance needs a path")
	}
	return winsec.SetInheritance(path, on, !clear)
}

// winDACLEntryFor renders a trustee's own entry the way the state
// declares it, or empty where it has none.
//
// Only an explicit entry. An inherited one belongs to the parent and
// cannot be changed here, so treating it as this path's own would make a
// state report a change it could not make and then fail to converge.
func winDACLEntryFor(path, trustee string) (string, error) {
	entries, err := winsec.Entries(path)
	if err != nil {
		return "", err
	}
	var found []string
	for _, e := range entries {
		if e.Inherited || !sameAccount(e.Trustee, trustee) {
			continue
		}
		found = append(found, fmt.Sprintf("%s %s (%s)", e.Kind, e.Permission, e.AppliesTo))
	}
	// More than one is legal and is what a hand-edited list looks like.
	// Reported as it is rather than collapsed, so a state that is about
	// to replace two entries with one says so.
	return strings.Join(found, ", "), nil
}

// sameAccountBySID resolves both accounts and reports whether they are
// the same SID. ok is false, asking sameAccount to fall back to the
// bare-name comparison, when either side does not resolve.
func sameAccountBySID(a, b string) (same, ok bool) {
	sidA, okA := accountSID(a)
	if !okA {
		return false, false
	}
	sidB, okB := accountSID(b)
	if !okB {
		return false, false
	}
	return sidA.Equals(sidB), true
}

// accountSID resolves an account to its SID, either of the two forms
// winsec hands back (describe, in internal/winsec/winsec_windows.go):
// a name, which needs a lookup, or the SID's own string form for an
// account that did not resolve there -- which StringToSid parses
// directly, with no lookup and nothing to reach a domain controller
// for. Tried first, because it is the cheap case and because
// LookupSID's own name resolution rejects a SID string outright
// ("No mapping between account names and security IDs was done"),
// confirmed on this host.
func accountSID(s string) (*windows.SID, bool) {
	trimmed := strings.TrimSpace(s)
	if sid, err := windows.StringToSid(trimmed); err == nil {
		return sid, true
	}
	sid, _, _, err := windows.LookupSID("", trimmed)
	return sid, err == nil
}
