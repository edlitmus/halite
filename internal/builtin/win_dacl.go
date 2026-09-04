package builtin

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// win_dacl, SPEC sections 15.3 and 15.5.
//
// Windows has no uid and gid, so `file`'s `user:` attribute and every
// state that sets ownership went through a refusal on this platform:
// the access control list is where the answer lives, and nothing read or
// wrote one. This is the module SPEC names for it, and wiring it in is
// what makes `file.managed` with a `win_perms:` block do something.
//
// The permission levels and the applies-to names are Salt's, because a
// tree being migrated already writes `perms: full_control` and
// `applies_to: this_folder_subfolders_files`. Renaming them would make
// the module correct and useless.
//
// The registration is here rather than in the platform file so that
// `sys.list_functions` names these on every platform and the signature's
// own Platforms field produces the refusal. A module that is absent off
// its platform makes "not written yet" and "you have mistyped it" the
// same message.

// winOnly is the platform restriction every function here carries.
var winOnly = []string{"windows"}

func registerWinDACL(r *Registries) {
	pathParam := nameParam("The file or directory. Defaults to the state ID.")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "get_owner",
				Doc:       "Return the account that owns a path.",
				Params:    []signature.Param{req("path", signature.String, "The file or directory.")},
				Returns:   "the account, as DOMAIN\\name",
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winDACLGetOwner(states.Str(args, "path", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "set_owner",
				Doc: "Make an account the owner of a path.",
				Params: []signature.Param{
					req("path", signature.String, "The file or directory."),
					req("owner", signature.String, "The account to make the owner."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "path", "")
				owner := states.Str(args, "owner", "")
				if c.Test {
					return true, nil
				}
				if err := winDACLSetOwner(path, owner); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "get_permissions",
				Doc: "Return a path's access control list, in the order Windows evaluates it.",
				Params: []signature.Param{
					req("path", signature.String, "The file or directory."),
				},
				Returns:   "a list of entries, each with trustee, kind, permission, applies_to, and inherited",
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winDACLEntries(states.Str(args, "path", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "has_permission",
				Doc: "Report whether a trustee is granted at least a permission on a path.",
				Params: []signature.Param{
					req("path", signature.String, "The file or directory."),
					req("trustee", signature.String, "The account."),
					opt("permission", signature.String, "full_control", "The level to test for."),
					opt("exact", signature.Bool, false,
						"Require the entry to be exactly this level rather than at least it."),
				},
				Returns:   "true when the trustee has it",
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winDACLHasPermission(
					states.Str(args, "path", ""),
					states.Str(args, "trustee", ""),
					states.Str(args, "permission", "full_control"),
					states.Bool(args, "exact", false))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "grant",
				Doc: "Give a trustee a permission on a path, replacing what it had.",
				Params: []signature.Param{
					req("path", signature.String, "The file or directory."),
					req("trustee", signature.String, "The account."),
					opt("permission", signature.String, "read", "The level to grant."),
					opt("applies_to", signature.String, "this_folder_subfolders_files",
						"What the entry covers, for a directory."),
					opt("deny", signature.Bool, false, "Write a deny entry rather than an allow."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := winDACLGrant(
					states.Str(args, "path", ""),
					states.Str(args, "trustee", ""),
					states.Str(args, "permission", "read"),
					states.Str(args, "applies_to", "this_folder_subfolders_files"),
					states.Bool(args, "deny", false))
				if err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "revoke",
				Doc: "Remove every entry a trustee has of its own on a path.",
				Params: []signature.Param{
					req("path", signature.String, "The file or directory."),
					req("trustee", signature.String, "The account."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				if err := winDACLRevoke(states.Str(args, "path", ""), states.Str(args, "trustee", "")); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "get_inheritance",
				Doc:       "Report whether a path takes entries from its parent.",
				Params:    []signature.Param{req("path", signature.String, "The file or directory.")},
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winDACLInherits(states.Str(args, "path", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "set_inheritance",
				Doc: "Turn inheritance from the parent on or off.",
				Params: []signature.Param{
					req("path", signature.String, "The file or directory."),
					req("enabled", signature.Bool, "Whether the parent's entries apply."),
					opt("clear", signature.Bool, false,
						"When turning it off, drop what was inherited rather than keeping it as this path's own."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := winDACLSetInheritance(
					states.Str(args, "path", ""),
					states.Bool(args, "enabled", true),
					states.Bool(args, "clear", false))
				if err != nil {
					return nil, err
				}
				return true, nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "present",
				Doc: "Ensure a trustee has exactly a permission on a path.",
				Params: []signature.Param{
					pathParam,
					req("trustee", signature.String, "The account."),
					opt("permission", signature.String, "read", "The level it should have."),
					opt("applies_to", signature.String, "this_folder_subfolders_files",
						"What the entry covers, for a directory."),
					opt("deny", signature.Bool, false, "Write a deny entry rather than an allow."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.5",
			},
			Fn: winDACLPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "absent",
				Doc: "Ensure a trustee has no entry of its own on a path.",
				Params: []signature.Param{
					pathParam,
					req("trustee", signature.String, "The account."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.5",
			},
			Fn: winDACLAbsent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "inherit",
				Doc: "Ensure a path does or does not take entries from its parent.",
				Params: []signature.Param{
					pathParam,
					opt("enabled", signature.Bool, true, "Whether the parent's entries apply."),
					opt("clear", signature.Bool, false,
						"When turning it off, drop what was inherited rather than keeping it as this path's own."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.5",
			},
			Fn: winDACLInherit,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "win_dacl", Function: "owner",
				Doc: "Ensure a path is owned by an account.",
				Params: []signature.Param{
					pathParam,
					req("owner", signature.String, "The account that should own it."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.5",
			},
			Fn: winDACLOwnerState,
		},
	)
}

// winDACLPresent ensures a trustee has exactly the permission asked for.
//
// Exactly, not at least. A state that says an account has `read` and
// leaves a `full_control` entry in place because it satisfies the read
// has not done what the file says, and the difference is the whole
// reason to declare it.
func winDACLPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	path := states.Str(args, "name", "")
	trustee := states.Str(args, "trustee", "")
	permission := states.Str(args, "permission", "read")
	scope := states.Str(args, "applies_to", "this_folder_subfolders_files")
	deny := states.Bool(args, "deny", false)
	if path == "" || trustee == "" {
		return states.False("win_dacl.present needs a path and a trustee."), nil
	}
	kind := "allow"
	if deny {
		kind = "deny"
	}

	current, err := winDACLEntryFor(path, trustee)
	if err != nil {
		return states.False(fmt.Sprintf("The permissions of %s could not be read: %v", path, err)), nil
	}
	want := fmt.Sprintf("%s %s (%s)", kind, permission, scope)
	if current == want {
		return states.True(fmt.Sprintf("%s already has %s on %s.", trustee, want, path)), nil
	}

	changes := value.NewMap(1)
	changes.Set(trustee, states.Change(currentOrNone(current), want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would have %s on %s.", trustee, want, path), changes), nil
	}
	if err := winDACLGrant(path, trustee, permission, scope, deny); err != nil {
		return states.False(fmt.Sprintf("%s could not be granted %s on %s: %v",
			trustee, want, path, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s now has %s on %s.", trustee, want, path), changes), nil
}

// winDACLAbsent ensures a trustee has no entry of its own.
func winDACLAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	path := states.Str(args, "name", "")
	trustee := states.Str(args, "trustee", "")
	if path == "" || trustee == "" {
		return states.False("win_dacl.absent needs a path and a trustee."), nil
	}

	current, err := winDACLEntryFor(path, trustee)
	if err != nil {
		return states.False(fmt.Sprintf("The permissions of %s could not be read: %v", path, err)), nil
	}
	if current == "" {
		return states.True(fmt.Sprintf("%s has no entry of its own on %s.", trustee, path)), nil
	}

	changes := value.NewMap(1)
	changes.Set(trustee, states.Change(current, "none"))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would lose %s on %s.", trustee, current, path), changes), nil
	}
	if err := winDACLRevoke(path, trustee); err != nil {
		return states.False(fmt.Sprintf("%s could not be revoked on %s: %v", trustee, path, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s no longer has an entry on %s.", trustee, path), changes), nil
}

// winDACLInherit ensures inheritance is on or off.
func winDACLInherit(c *exec.Context, args *value.Map) (states.Result, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return states.False("win_dacl.inherit needs a path."), nil
	}
	want := states.Bool(args, "enabled", true)
	clear := states.Bool(args, "clear", false)

	current, err := winDACLInherits(path)
	if err != nil {
		return states.False(fmt.Sprintf("The permissions of %s could not be read: %v", path, err)), nil
	}
	if current == want {
		return states.True(fmt.Sprintf("%s already %s its parent's permissions.",
			path, inheritWord(want))), nil
	}

	changes := value.NewMap(1)
	changes.Set("inheritance", states.Change(current, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would %s its parent's permissions.",
			path, inheritWord(want)), changes), nil
	}
	if err := winDACLSetInheritance(path, want, clear); err != nil {
		return states.False(fmt.Sprintf("The inheritance of %s could not be changed: %v", path, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s now %s its parent's permissions.",
		path, inheritWord(want)), changes), nil
}

// winDACLOwnerState ensures a path is owned by an account.
func winDACLOwnerState(c *exec.Context, args *value.Map) (states.Result, error) {
	path := states.Str(args, "name", "")
	want := states.Str(args, "owner", "")
	if path == "" || want == "" {
		return states.False("win_dacl.owner needs a path and an owner."), nil
	}

	current, err := winDACLGetOwner(path)
	if err != nil {
		return states.False(fmt.Sprintf("The owner of %s could not be read: %v", path, err)), nil
	}
	if sameAccount(current, want) {
		return states.True(fmt.Sprintf("%s is already owned by %s.", path, current)), nil
	}

	changes := value.NewMap(1)
	changes.Set("owner", states.Change(current, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be owned by %s.", path, want), changes), nil
	}
	if err := winDACLSetOwner(path, want); err != nil {
		return states.False(fmt.Sprintf("The owner of %s could not be changed: %v", path, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s is now owned by %s.", path, want), changes), nil
}

func inheritWord(on bool) string {
	if on {
		return "inherits"
	}
	return "does not inherit"
}

func currentOrNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// sameAccount compares two account names the way Windows does.
//
// Case-insensitively, and treating a bare name as the machine's own:
// `Administrators` and `BUILTIN\Administrators` name one group, and a
// state that reported a change between them would never converge.
func sameAccount(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	return strings.EqualFold(bareAccount(a), bareAccount(b))
}

func bareAccount(s string) string {
	if i := strings.LastIndex(s, `\`); i >= 0 {
		return s[i+1:]
	}
	return s
}
