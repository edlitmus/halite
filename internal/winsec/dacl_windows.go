//go:build windows

package winsec

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The access control list vocabulary the win_dacl module of SPEC 15.3
// needs: reading a list, granting and revoking one trustee's access,
// setting an owner, and turning inheritance on and off.
//
// Salt's own names are used for the permission levels and for the scope
// an entry applies to, because a tree being migrated already says
// `perms: full_control` and `applies_to: this_folder_subfolders_files`,
// and renaming them would make the module correct and useless.

// Permission levels, in Windows' own terms.
//
// These are the five the property sheet shows and the five Salt names.
// Each is a fixed combination of the granular rights; the numbers are
// stated here rather than referred to, because an entry that grants
// 0x1301BF and one that grants "modify" are the same entry and an
// operator reading a diff needs to see that.
const (
	// permFullControl is FILE_ALL_ACCESS: everything, including changing
	// the list itself and taking ownership.
	permFullControl = 0x1F01FF
	// permModify is read, write, execute and delete — everything except
	// changing the list and taking ownership.
	permModify = 0x1301BF
	// permReadExecute is read plus the right to run a file.
	permReadExecute = 0x1200A9
	// permRead is read only.
	permRead = 0x120089
	// permWrite is write only, which on its own is less useful than it
	// looks: it cannot list a directory it writes into.
	permWrite = 0x100116
)

// permissionNames maps Salt's names onto those masks. Ordered longest
// first when rendering, so a mask that is exactly full_control is not
// described as "read, write, ...".
var permissionNames = []struct {
	name string
	mask uint32
}{
	{"full_control", permFullControl},
	{"modify", permModify},
	{"read_execute", permReadExecute},
	{"read", permRead},
	{"write", permWrite},
}

// PermissionMask resolves a permission name, or a raw mask written as
// hex or decimal, to the mask itself.
//
// A raw mask is accepted because the five names do not cover every
// combination Windows can express, and a tree that needs one should not
// have to wait for a name to be invented for it. It has to be written
// deliberately — `0x1301BF`, not `1301BF` — so that a typo in a name is
// not silently read as a number.
func PermissionMask(perm string) (uint32, error) {
	want := strings.ToLower(strings.TrimSpace(perm))
	for _, p := range permissionNames {
		if p.name == want {
			return p.mask, nil
		}
	}
	if strings.HasPrefix(want, "0x") {
		n, err := strconv.ParseUint(want[2:], 16, 32)
		if err == nil {
			return uint32(n), nil
		}
	}
	names := make([]string, 0, len(permissionNames))
	for _, p := range permissionNames {
		names = append(names, p.name)
	}
	return 0, fmt.Errorf("%q is not a permission; this build understands %s, "+
		"or an access mask written as hex such as 0x1301BF",
		perm, strings.Join(names, ", "))
}

// PermissionName renders a mask the way an operator reads it: the level
// it exactly matches, or the hex if it matches none.
//
// Never an approximation. A mask that is full_control minus one bit is
// not "full_control", and describing it as one would make a comparison
// that should have reported a difference report none.
func PermissionName(mask uint32) string {
	for _, p := range permissionNames {
		if p.mask == mask {
			return p.name
		}
	}
	return fmt.Sprintf("0x%06X", mask)
}

// Inheritance scopes, in Salt's names. The pair of flags each one sets
// is the whole of what Windows records.
var inheritanceScopes = []struct {
	name  string
	flags uint32
}{
	{"this_folder_only", 0},
	{"this_folder_subfolders_files", windows.CONTAINER_INHERIT_ACE | windows.OBJECT_INHERIT_ACE},
	{"this_folder_subfolders", windows.CONTAINER_INHERIT_ACE},
	{"this_folder_files", windows.OBJECT_INHERIT_ACE},
	{"subfolders_files", windows.CONTAINER_INHERIT_ACE | windows.OBJECT_INHERIT_ACE | windows.INHERIT_ONLY_ACE},
	{"subfolders_only", windows.CONTAINER_INHERIT_ACE | windows.INHERIT_ONLY_ACE},
	{"files_only", windows.OBJECT_INHERIT_ACE | windows.INHERIT_ONLY_ACE},
}

// ScopeFlags resolves an applies-to name to its inheritance flags.
func ScopeFlags(scope string) (uint32, error) {
	want := strings.ToLower(strings.TrimSpace(scope))
	if want == "" {
		// What the property sheet defaults to, and what a tree means
		// when it does not say.
		return windows.CONTAINER_INHERIT_ACE | windows.OBJECT_INHERIT_ACE, nil
	}
	for _, s := range inheritanceScopes {
		if s.name == want {
			return s.flags, nil
		}
	}
	names := make([]string, 0, len(inheritanceScopes))
	for _, s := range inheritanceScopes {
		names = append(names, s.name)
	}
	return 0, fmt.Errorf("%q is not a scope; this build understands %s",
		scope, strings.Join(names, ", "))
}

// ScopeName renders inheritance flags as the name a tree writes.
func ScopeName(flags uint32) string {
	only := flags & (windows.CONTAINER_INHERIT_ACE | windows.OBJECT_INHERIT_ACE | windows.INHERIT_ONLY_ACE)
	for _, s := range inheritanceScopes {
		if s.flags == only {
			return s.name
		}
	}
	return fmt.Sprintf("0x%02X", only)
}

// ACE is one entry in a path's access control list.
type ACE struct {
	// Trustee is the account, as an administrator reads it.
	Trustee string
	// Kind is "allow" or "deny".
	Kind string
	// Permission is the level's name, or the hex mask where it is not
	// one of the five.
	Permission string
	// Mask is the access mask itself, for a caller comparing exactly.
	Mask uint32
	// AppliesTo is the scope, in the name a tree writes.
	AppliesTo string
	// Inherited marks an entry that comes from the parent rather than
	// from this path. It cannot be removed here — the parent owns it —
	// and a state that tried would report a change that never happened.
	Inherited bool
}

// Entries reads a path's access control list, in the order Windows
// evaluates it.
//
// The order is preserved rather than sorted. A deny entry before an
// allow entry is the difference between access and no access, and a
// listing that reordered them would be describing a different list.
func Entries(path string) ([]ACE, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("reading the permissions of %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, fmt.Errorf("reading the permissions of %s: %w", path, err)
	}
	if dacl == nil {
		// A NULL list grants everyone everything, which is not the same
		// as an empty one granting nobody anything. Saying so as an
		// entry keeps the difference visible.
		return []ACE{{
			Trustee: "Everyone", Kind: "allow",
			Permission: PermissionName(permFullControl), Mask: permFullControl,
			AppliesTo: ScopeName(windows.CONTAINER_INHERIT_ACE | windows.OBJECT_INHERIT_ACE),
		}}, nil
	}

	out := make([]ACE, 0, dacl.AceCount)
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, fmt.Errorf("reading the permissions of %s: %w", path, err)
		}
		kind := ""
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			kind = "allow"
		case windows.ACCESS_DENIED_ACE_TYPE:
			kind = "deny"
		default:
			// An audit or an object entry. Not this module's to report,
			// and skipping it silently would make a listing that says
			// "these are the entries" untrue — so it is named.
			kind = fmt.Sprintf("other(type %d)", ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		out = append(out, ACE{
			Trustee:    describe(sid),
			Kind:       kind,
			Permission: PermissionName(uint32(ace.Mask)),
			Mask:       uint32(ace.Mask),
			AppliesTo:  ScopeName(uint32(ace.Header.AceFlags)),
			Inherited:  ace.Header.AceFlags&windows.INHERITED_ACE != 0,
		})
	}
	return out, nil
}

// lookupTrustee resolves an account name to a SID.
//
// The name is resolved rather than stored, so that a list written
// against `BUILTIN\Administrators` on one machine means the same thing
// on another. A name that does not resolve is an error naming it: a
// state that granted access to an account that does not exist would
// otherwise appear to work and grant nothing.
func lookupTrustee(name string) (*windows.SID, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, fmt.Errorf("no account was named")
	}
	// A SID written out is accepted too, for an account with no name on
	// this machine — a domain account on a disconnected host, or one
	// whose name has since been reused.
	if strings.HasPrefix(strings.ToUpper(trimmed), "S-1-") {
		sid, err := windows.StringToSid(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%q is not a usable SID: %w", trimmed, err)
		}
		return sid, nil
	}
	sid, _, _, err := windows.LookupSID("", trimmed)
	if err != nil {
		return nil, fmt.Errorf("there is no account called %q on this machine or its domain: %w", trimmed, err)
	}
	return sid, nil
}

// entry is one access control entry with its account kept as a SID.
//
// EXPLICIT_ACCESS holds its trustee as a uintptr, so recovering the SID
// from one means converting a uintptr back to a pointer — which vet
// flags, and rightly: nothing keeps the memory it refers to alive. The
// SID is carried alongside instead, and the EXPLICIT_ACCESS is built
// only at the moment of writing, from a value the caller still holds.
type entry struct {
	sid   *windows.SID
	mask  uint32
	mode  windows.ACCESS_MODE
	flags uint32
}

// explicit builds the EXPLICIT_ACCESS the API takes.
func (e entry) explicit() windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.ACCESS_MASK(e.mask),
		AccessMode:        e.mode,
		Inheritance:       e.flags,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(e.sid),
		},
	}
}

// readEntries reads a path's list as entries this package can rewrite.
//
// Every write replaces the whole list, because that is the only
// operation the API offers, so a read has to come first. inheritedToo
// decides whether the parent's contribution is included: normally it is
// not, since writing an inherited entry back as an explicit one freezes
// a copy of the parent's list into the child, and a later change to the
// parent then stops reaching it — silently, and only visible the next
// time somebody wondered why. Turning inheritance off while keeping the
// access is the one case that wants them.
func readEntries(path string, inheritedToo bool) ([]entry, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("reading the permissions of %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, fmt.Errorf("reading the permissions of %s: %w", path, err)
	}
	if dacl == nil {
		return nil, nil
	}
	var out []entry
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, fmt.Errorf("reading the permissions of %s: %w", path, err)
		}
		if !inheritedToo && ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			continue
		}
		mode := windows.ACCESS_MODE(windows.GRANT_ACCESS)
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		case windows.ACCESS_DENIED_ACE_TYPE:
			mode = windows.DENY_ACCESS
		default:
			// An audit or an object entry. This module does not write
			// them and cannot faithfully rewrite them, so carrying one
			// through a replace would corrupt it.
			continue
		}
		// The SIDs in a descriptor point into its own memory, which is
		// freed when it goes out of scope. A list built from them and
		// written afterwards would refer to memory that is gone.
		copied, err := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).Copy()
		if err != nil {
			return nil, fmt.Errorf("copying an account identifier: %w", err)
		}
		out = append(out, entry{
			sid:   copied,
			mask:  uint32(ace.Mask),
			mode:  mode,
			flags: uint32(ace.Header.AceFlags) & (windows.CONTAINER_INHERIT_ACE | windows.OBJECT_INHERIT_ACE | windows.INHERIT_ONLY_ACE),
		})
	}
	return out, nil
}

// emptyACL is a present but empty access control list.
//
// It exists because the difference between an empty list and no list is
// the difference between "nobody may open this" and "everyone may do
// anything to it", and the obvious code writes the second by accident.
// windows.ACLFromEntries with no entries asks SetEntriesInAcl for a list
// of nothing, gets NULL back with success, and then dereferences it —
// so the empty case cannot go through it at all. Passing that NULL on to
// SetNamedSecurityInfo would not crash: it would quietly set a NULL DACL
// and grant Everyone full control on whatever a state was trying to
// lock down.
//
// The structure is fixed and eight bytes: revision, a reserved byte, the
// size, the count of entries, and two reserved bytes. ACL_REVISION is 2.
func emptyACL() *windows.ACL {
	const aclRevision = 2
	buf := make([]byte, 8)
	buf[0] = aclRevision
	// AclSize, little-endian, at offset 2: the header and nothing else.
	buf[2] = 8
	return (*windows.ACL)(unsafe.Pointer(&buf[0]))
}

// writeEntries replaces a path's list, keeping or discarding what the
// parent contributes.
func writeEntries(path string, entries []entry, inherit bool) error {
	var acl *windows.ACL
	if len(entries) == 0 {
		acl = emptyACL()
	} else {
		explicit := make([]windows.EXPLICIT_ACCESS, 0, len(entries))
		for _, e := range entries {
			explicit = append(explicit, e.explicit())
		}
		var err error
		if acl, err = windows.ACLFromEntries(explicit, nil); err != nil {
			return fmt.Errorf("building an access control list for %s: %w", path, err)
		}
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	if inherit {
		info |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	} else {
		info |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		info, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("setting the permissions of %s: %w", path, err)
	}
	return nil
}

// Grant gives a trustee a permission, replacing whatever that trustee
// had before.
//
// Replacing rather than adding: two allow entries for the same account
// are the union of their masks, so adding `read` beside an existing
// `full_control` reads as a tightening and is not one. A state that says
// an account has `read` means it has read, and this makes that true.
func Grant(path, trustee, permission, scope string, deny bool) error {
	sid, err := lookupTrustee(trustee)
	if err != nil {
		return err
	}
	mask, err := PermissionMask(permission)
	if err != nil {
		return err
	}
	flags, err := ScopeFlags(scope)
	if err != nil {
		return err
	}
	existing, err := readEntries(path, false)
	if err != nil {
		return err
	}
	kept := make([]entry, 0, len(existing)+1)
	for _, e := range existing {
		if !e.sid.Equals(sid) {
			kept = append(kept, e)
		}
	}
	mode := windows.ACCESS_MODE(windows.GRANT_ACCESS)
	if deny {
		mode = windows.DENY_ACCESS
	}
	kept = append(kept, entry{sid: sid, mask: mask, mode: mode, flags: flags})
	return writeEntries(path, kept, inheritsFromParent(path))
}

// Revoke removes every entry a trustee has of its own on a path.
//
// Only the explicit ones: an inherited entry belongs to the parent, and
// removing it here would mean freezing a copy of the parent's list into
// this path. Revoke says so rather than reporting success and leaving
// the account with access.
func Revoke(path, trustee string) error {
	sid, err := lookupTrustee(trustee)
	if err != nil {
		return err
	}
	existing, err := readEntries(path, false)
	if err != nil {
		return err
	}
	kept := make([]entry, 0, len(existing))
	removed := false
	for _, e := range existing {
		if e.sid.Equals(sid) {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		// Not an error on its own: a state asking for an account to have
		// no access is satisfied by an account that already has none.
		// But an inherited entry is access this cannot remove, and
		// reporting success would be reporting a lock that is not there.
		if inherited, err := inheritedAccessFor(path, sid); err == nil && inherited != "" {
			return fmt.Errorf(
				"%s has no entry of its own on %s, but inherits %s from the parent directory; "+
					"remove it there, or turn inheritance off on this path first",
				trustee, path, inherited)
		}
		return nil
	}
	return writeEntries(path, kept, inheritsFromParent(path))
}

// inheritedAccessFor reports what a trustee gets from the parent, so
// that a revoke that cannot help says why.
func inheritedAccessFor(path string, sid *windows.SID) (string, error) {
	entries, err := Entries(path)
	if err != nil {
		return "", err
	}
	name := describe(sid)
	for _, e := range entries {
		if e.Inherited && e.Trustee == name {
			return e.Permission, nil
		}
	}
	return "", nil
}

// Inherits reports whether a path takes entries from its parent.
func Inherits(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("reading the permissions of %s: %w", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		return false, fmt.Errorf("reading the permissions of %s: %w", path, err)
	}
	// The flag records that the list is *protected* from the parent, so
	// inheritance is its absence.
	return control&windows.SE_DACL_PROTECTED == 0, nil
}

// inheritsFromParent is Inherits for a caller that has nowhere to report
// a failure and must not change the answer by asking. A path whose state
// cannot be read is left inheriting, which is the Windows default and
// the less surprising of the two.
func inheritsFromParent(path string) bool {
	on, err := Inherits(path)
	if err != nil {
		return true
	}
	return on
}

// SetInheritance turns inheritance from the parent on or off.
//
// When turning it off, keep decides what happens to the entries the
// parent was contributing: copied into this path as its own, or dropped.
// That is the choice the property sheet puts in a dialog box, and
// getting it wrong is how a directory ends up with no entries at all and
// nobody but its owner able to open it.
func SetInheritance(path string, on, keep bool) error {
	if on {
		// Turning it back on: the explicit entries stay, and the
		// parent's are added to them by the system.
		entries, err := readEntries(path, false)
		if err != nil {
			return err
		}
		return writeEntries(path, entries, true)
	}
	entries, err := readEntries(path, keep)
	if err != nil {
		return err
	}
	return writeEntries(path, entries, false)
}

// SetOwner makes an account the owner of a path.
//
// Needs a privilege the process may not hold, and says which. Setting
// the owner to somebody other than yourself needs SeRestorePrivilege;
// taking ownership yourself needs SeTakeOwnershipPrivilege or WRITE_OWNER
// on the object. Both are held by an administrator and neither is
// enabled in a token by default, so they are enabled here for the call
// and the failure names them.
func SetOwner(path, trustee string) error {
	sid, err := lookupTrustee(trustee)
	if err != nil {
		return err
	}
	restore := enablePrivilege("SeRestorePrivilege")
	takeOwnership := enablePrivilege("SeTakeOwnershipPrivilege")

	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION, sid, nil, nil, nil)
	if err == nil {
		return nil
	}
	hint := ""
	if !restore && !takeOwnership {
		hint = "; this process holds neither SeRestorePrivilege nor SeTakeOwnershipPrivilege, " +
			"which is what setting an owner needs — run the node as an administrator"
	} else if !restore {
		hint = "; setting the owner to another account needs SeRestorePrivilege, " +
			"which this process does not hold"
	}
	return fmt.Errorf("setting the owner of %s to %s: %w%s", path, trustee, err, hint)
}

// enablePrivilege turns on one privilege in this process's token, and
// reports whether it is now held.
//
// A token carries the privileges an account may use and enables few of
// them; a privilege that is present but disabled is not applied. This is
// the same thing takeown.exe and icacls do before they touch an owner.
func enablePrivilege(name string) bool {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()

	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, wide, &luid); err != nil {
		return false
	}
	privileges := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{{
			Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED,
		}},
	}
	if err := windows.AdjustTokenPrivileges(token, false, &privileges, 0, nil, nil); err != nil {
		return false
	}
	// AdjustTokenPrivileges reports success when it enabled *some* of
	// what it was asked for, including none of it. The last error is the
	// only way to tell.
	return windows.GetLastError() != windows.ERROR_NOT_ALL_ASSIGNED
}

// PermissionLevels are the names this build understands, for a message
// that has to list them.
func PermissionLevels() []string {
	out := make([]string, 0, len(permissionNames))
	for _, p := range permissionNames {
		out = append(out, p.name)
	}
	return out
}

// Scopes are the applies-to names this build understands.
func Scopes() []string {
	out := make([]string, 0, len(inheritanceScopes))
	for _, s := range inheritanceScopes {
		out = append(out, s.name)
	}
	sort.Strings(out)
	return out
}
