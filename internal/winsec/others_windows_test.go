//go:build windows

package winsec

import (
	"encoding/csv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// The access-list checks issue 608 item 3 read and did not run. Each test
// here is one claim from that issue, put to a real security descriptor on
// a real NTFS volume, so that the check is measured against what Windows
// stores rather than against what its documentation says it stores.

// users is BUILTIN\Users by SID, so that the tests do not depend on the
// machine's language: the name is localised and the SID is not.
const users = "S-1-5-32-545"

// privateFile writes a file and restricts it to its owner, SYSTEM and
// Administrators, which is the state every Others check starts from.
func privateFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restrict(path); err != nil {
		t.Fatalf("restricting %s: %v", path, err)
	}
	return path
}

func mentionsUsers(names []string) bool {
	for _, n := range names {
		if strings.HasSuffix(n, `\Users`) || n == "Users" || n == users {
			return true
		}
	}
	return false
}

// An account that may delete a file, or append to it, can alter what a
// private file says without ever reading it: delete it and write another
// in its place, or add a line to the end of a key list. Others is the
// check that says a file is private, so such an account has to be named.
func TestOthersNamesAnAccountThatCanAlterTheFileWithoutReadingIt(t *testing.T) {
	for _, c := range []struct {
		what string
		mask string
	}{
		{"DELETE", "0x010000"},
		{"FILE_APPEND_DATA", "0x000004"},
		{"FILE_WRITE_ATTRIBUTES", "0x000100"},
		{"FILE_WRITE_EA", "0x000010"},
	} {
		t.Run(c.what, func(t *testing.T) {
			path := privateFile(t)
			if err := Grant(path, users, c.mask, "this_folder_only", false); err != nil {
				t.Fatalf("granting Users %s: %v", c.what, err)
			}
			entries, _ := Entries(path)
			others, err := Others(path)
			if err != nil {
				t.Fatal(err)
			}
			if !mentionsUsers(others) {
				t.Errorf("Users was granted %s (%s) and Others reported %v; the list is %+v",
					c.what, c.mask, others, entries)
			}
		})
	}
}

// An inherit-only entry on a file applies to nothing: a file has no
// children to pass it to. Reporting it calls a private file open.
//
// The first run of this test granted the entry through Grant, and it
// passed on the old code too. So the entry is written here in SDDL,
// and the test first checks that NTFS kept it. Without that check, the
// test would pass because the entry was never stored.
func TestOthersIgnoresAnInheritOnlyEntryOnAFile(t *testing.T) {
	path := privateFile(t)
	setSDDL(t, path, ownerSDDL(t, path)+`(A;OIIO;FR;;;BU)`)
	entries, err := Entries(path)
	if err != nil {
		t.Fatal(err)
	}
	stored := false
	for _, e := range entries {
		if mentionsUsers([]string{e.Trustee}) && e.AppliesTo == "files_only" {
			stored = true
		}
	}
	if !stored {
		t.Fatalf("NTFS did not keep an inherit-only entry on a file, so there is nothing to test; the list is %+v", entries)
	}
	others, err := Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 0 {
		t.Errorf("an inherit-only entry made a file look open to %v; the list is %+v", others, entries)
	}
}

// On a directory the same entry is not inert: it is what every file
// created inside it will be given. A directory halite calls private is a
// promise about what is put in it, so the entry is reported there.
func TestOthersReportsAnInheritOnlyEntryOnADirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RestrictDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := Grant(dir, users, "read", "files_only", false); err != nil {
		t.Fatalf("granting Users an inherit-only read: %v", err)
	}
	others, err := Others(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !mentionsUsers(others) {
		t.Errorf("a directory that gives Users read on every new file was reported as private: %v", others)
	}
}

// setSDDL replaces path's list with one written in SDDL, protected from
// the parent. It is how the tests below put entry types onto a file that
// nothing in this package writes.
func setSDDL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("parsing %q: %v", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("reading the list out of %q: %v", sddl, err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatalf("NTFS refused the list %q: %v", sddl, err)
	}
}

// ownerSDDL is the list a private file has, in SDDL, naming the owner by
// SID rather than by OW: OW is the OWNER RIGHTS principal, which is a
// different SID and one Others would rightly report.
func ownerSDDL(t *testing.T, path string) string {
	t.Helper()
	own, err := owner(path)
	if err != nil {
		t.Fatal(err)
	}
	return "D:P(A;;FA;;;" + own.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)"
}

// A conditional ("callback") allow entry grants its access whenever its
// condition holds. Others skipped every entry that was not a plain allow,
// so a file readable by Users under a condition Users meets was private.
func TestOthersNamesAConditionalGrant(t *testing.T) {
	path := privateFile(t)
	// The condition is true for every member of Users, which is the
	// point: it is a grant in all but its spelling.
	setSDDL(t, path, ownerSDDL(t, path)+`(XA;;FR;;;BU;(Member_of {SID(BU)}))`)
	entries, _ := Entries(path)
	others, err := Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if !mentionsUsers(others) {
		t.Errorf("a conditional read grant to Users was not reported: %v; the list is %+v", others, entries)
	}
}

// An object entry carries a flags word and up to two GUIDs before its
// SID. Reading it at the layout of a plain entry names whatever those
// bytes happen to spell rather than the account the entry is for.
func TestEntriesNamesTheAccountOfAnObjectEntry(t *testing.T) {
	path := privateFile(t)
	// The GUID is the user class from the Active Directory schema. On a
	// file it means nothing, which is why the account is what is checked.
	setSDDL(t, path, ownerSDDL(t, path)+`(OA;;FR;bf967aba-0de6-11d0-a285-00aa003e49e2;;BU)`)
	entries, err := Entries(path)
	if err != nil {
		t.Fatal(err)
	}
	var trustees []string
	for _, e := range entries {
		trustees = append(trustees, e.Trustee+" ("+e.Kind+")")
	}
	if !mentionsUsers(trustees) {
		t.Errorf("the object entry for Users was listed under another name; the trustees are %v", trustees)
	}
	others, err := Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if !mentionsUsers(others) {
		t.Errorf("an object entry granting Users read was not reported by Others: %v", others)
	}
}

// heldPrivileges asks whoami, not this package, which privileges the
// token carries, so that the answer is not the code under test agreeing
// with itself.
func heldPrivileges(t *testing.T) map[string]bool {
	t.Helper()
	out, err := exec.Command("whoami", "/priv", "/fo", "csv", "/nh").Output()
	if err != nil {
		t.Fatalf("whoami /priv: %v", err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		t.Fatalf("reading whoami's CSV %q: %v", out, err)
	}
	held := map[string]bool{}
	for _, r := range rows {
		if len(r) > 0 {
			held[strings.TrimSpace(r[0])] = true
		}
	}
	if len(held) == 0 {
		t.Fatalf("whoami listed no privileges at all: %q", out)
	}
	return held
}

// enablePrivilege must say no for a privilege the token does not carry.
// SetOwner's error names the missing privilege from this answer, so a
// yes for a privilege that is not there sends an operator looking for
// the wrong cause.
//
// The CI legs run the suite as a standard account, whose token carries
// neither SeRestorePrivilege nor SeTakeOwnershipPrivilege; whoami says so
// independently, and the expectation is taken from it.
func TestEnablePrivilegeSaysNoForAPrivilegeTheTokenLacks(t *testing.T) {
	held := heldPrivileges(t)
	t.Logf("whoami /priv lists %d privileges for this token", len(held))
	for _, name := range []string{
		"SeChangeNotifyPrivilege", // every account's token carries it
		"SeRestorePrivilege",
		"SeTakeOwnershipPrivilege",
		"SeTcbPrivilege", // only SYSTEM's does
	} {
		if got, want := enablePrivilege(name), held[name]; got != want {
			t.Errorf("enablePrivilege(%s) = %v; whoami says the token carries it: %v", name, got, want)
		}
	}
}
