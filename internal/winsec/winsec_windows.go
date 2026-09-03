//go:build windows

package winsec

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fullControl is what each of the three permitted trustees gets: the
// same authority mode 0600 gives a unix owner, which is everything.
const fullControl = windows.GENERIC_ALL

// readish is the set of rights that mean a trustee can see the
// contents. A trustee granted any of them is a trustee the file is not
// private from.
const readish = windows.GENERIC_READ | windows.GENERIC_ALL |
	windows.GENERIC_EXECUTE | windows.GENERIC_WRITE |
	windows.FILE_READ_DATA | windows.FILE_WRITE_DATA |
	windows.READ_CONTROL | windows.WRITE_DAC | windows.WRITE_OWNER

// wellKnown returns the SIDs that are allowed to reach any file halite
// keeps private: the machine account services run as, and the group
// that could take ownership of the file regardless of what the list
// says. Excluding them would be theatre.
func wellKnown() ([]*windows.SID, error) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("looking up the SYSTEM account: %w", err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("looking up the Administrators group: %w", err)
	}
	return []*windows.SID{system, admins}, nil
}

// owner reads a file's owner.
func owner(path string) (*windows.SID, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("reading the owner of %s: %w", path, err)
	}
	sid, _, err := sd.Owner()
	if err != nil {
		return nil, fmt.Errorf("reading the owner of %s: %w", path, err)
	}
	return sid, nil
}

// Restrict replaces path's access control list with one that grants the
// file's owner, SYSTEM and Administrators full control and grants
// nobody else anything.
//
// The list is marked protected, so the entries the parent directory
// would otherwise contribute — which is how a file under a
// world-readable directory becomes world-readable — do not apply.
func Restrict(path string) error {
	own, err := owner(path)
	if err != nil {
		return err
	}
	trustees, err := wellKnown()
	if err != nil {
		return err
	}
	trustees = append([]*windows.SID{own}, trustees...)

	entries := make([]windows.EXPLICIT_ACCESS, 0, len(trustees))
	for _, sid := range trustees {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: fullControl,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("building an access control list for %s: %w", path, err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
	if err != nil {
		return fmt.Errorf("restricting %s: %w", path, err)
	}
	return nil
}

// Others lists the accounts, beyond the owner and the two well-known
// ones, that are granted enough to read or alter path. It is empty for a
// file only its owner can reach.
//
// The names are resolved because the error a caller writes has to be
// actionable, and an unresolved SID in a message sends the reader to a
// search engine rather than to the file.
func Others(path string) ([]string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("reading the permissions of %s: %w", path, err)
	}
	own, _, err := sd.Owner()
	if err != nil {
		return nil, fmt.Errorf("reading the owner of %s: %w", path, err)
	}
	allowed, err := wellKnown()
	if err != nil {
		return nil, err
	}
	allowed = append(allowed, own)

	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, fmt.Errorf("reading the permissions of %s: %w", path, err)
	}
	if dacl == nil {
		// A NULL list grants everyone everything. It is the most open a
		// file can be, and it is not the absence of an answer.
		return []string{"Everyone"}, nil
	}

	var out []string
	seen := map[string]bool{}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, fmt.Errorf("reading the permissions of %s: %w", path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			// A deny entry cannot widen access, and this build does not
			// write one, so it is not a reason to call a file open.
			continue
		}
		if ace.Mask&readish == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if isAnyOf(sid, allowed) {
			continue
		}
		name := describe(sid)
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

// isAnyOf reports whether sid is one of the SIDs a private file may
// grant.
func isAnyOf(sid *windows.SID, allowed []*windows.SID) bool {
	for _, a := range allowed {
		if sid.Equals(a) {
			return true
		}
	}
	return false
}

// describe names a SID the way an administrator would, falling back to
// its string form for one that does not resolve — a deleted account, or
// one from a domain this machine cannot reach.
func describe(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	if domain == "" {
		return account
	}
	return domain + `\` + account
}

// Owner names the account that owns a path, in the form an
// administrator reads: `DOMAIN\account`, or the SID's string form for
// one that does not resolve.
//
// A Windows file has an owner, and halite was reporting that it had
// none: addOwnership did nothing off unix, so `file.stats` returned no
// `user` field and `file.get_user` answered "has no owner this platform
// reports" for every file on the platform.
func Owner(path string) (string, error) {
	sid, err := owner(path)
	if err != nil {
		return "", err
	}
	return describe(sid), nil
}
