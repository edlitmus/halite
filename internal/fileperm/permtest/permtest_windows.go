package permtest

import (
	"testing"

	"golang.org/x/sys/windows"

	"github.com/edlitmus/halite/internal/winsec"
)

// OpenToEveryone makes the file readable by every account.
func OpenToEveryone(t *testing.T, path string) {
	t.Helper()
	grant(t, path, windows.WinWorldSid, windows.GENERIC_READ, windows.GRANT_ACCESS)
}

// MakePrivate makes the file reachable by its owner, SYSTEM and
// Administrators alone.
func MakePrivate(t *testing.T, path string) {
	t.Helper()
	if err := winsec.Restrict(path); err != nil {
		t.Fatal(err)
	}
}

// DenyWrite makes a directory one this process cannot create a file in.
//
// A deny entry for the calling user, because Administrators can
// otherwise take ownership and the test would prove nothing. The entry
// is inherited by anything created inside, which is what makes the
// probe fail rather than the directory listing.
func DenyWrite(t *testing.T, dir string) {
	t.Helper()
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_WRITE,
		AccessMode:        windows.DENY_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user),
		},
	}}, currentDACL(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := setDACL(dir, acl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = winsec.Restrict(dir) })
}

func grant(t *testing.T, path string, sid windows.WELL_KNOWN_SID_TYPE,
	mask windows.ACCESS_MASK, mode windows.ACCESS_MODE) {
	t.Helper()
	who, err := windows.CreateWellKnownSid(sid)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        mode,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(who),
		},
	}}, currentDACL(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if err := setDACL(path, acl); err != nil {
		t.Fatal(err)
	}
}

func currentDACL(t *testing.T, path string) *windows.ACL {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	return dacl
}

func setDACL(path string, acl *windows.ACL) error {
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
}

func currentUserSID() (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

// AllowWrite undoes DenyWrite. The list is replaced rather than edited:
// a deny entry is evaluated before every grant, so leaving one in place
// and adding a grant beside it changes nothing.
func AllowWrite(t *testing.T, dir string) {
	t.Helper()
	if err := winsec.Restrict(dir); err != nil {
		t.Fatal(err)
	}
}
