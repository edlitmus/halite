//go:build windows

package winsec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A permission level round-trips through its name and its mask.
//
// The mask is what Windows stores and the name is what a tree writes, so
// a mapping that is wrong in either direction is a state that grants
// something other than what it says.
func TestAPermissionLevelRoundTrips(t *testing.T) {
	for _, name := range PermissionLevels() {
		mask, err := PermissionMask(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := PermissionName(mask); got != name {
			t.Errorf("%s -> 0x%X -> %s", name, mask, got)
		}
	}

	// A raw mask is accepted where the five names do not reach, and
	// comes back as hex rather than as an approximation to a name.
	mask, err := PermissionMask("0x1301BF")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := PermissionMask("modify"); mask != got {
		t.Errorf("0x1301BF is modify; got 0x%X and 0x%X", mask, got)
	}
	odd, err := PermissionMask("0x000001")
	if err != nil {
		t.Fatal(err)
	}
	if got := PermissionName(odd); got != "0x000001" {
		t.Errorf("a mask matching no level rendered as %q", got)
	}

	// A typo is an error naming the alternatives, not a silent zero.
	if _, err := PermissionMask("full-control"); err == nil {
		t.Error("a misspelled level was accepted")
	} else if !strings.Contains(err.Error(), "full_control") {
		t.Errorf("the error does not offer the real names: %v", err)
	}
}

func TestAScopeRoundTrips(t *testing.T) {
	for _, name := range Scopes() {
		flags, err := ScopeFlags(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := ScopeName(flags); got != name {
			t.Errorf("%s -> 0x%X -> %s", name, flags, got)
		}
	}
	// Unstated means what the property sheet defaults to.
	flags, err := ScopeFlags("")
	if err != nil {
		t.Fatal(err)
	}
	if got := ScopeName(flags); got != "this_folder_subfolders_files" {
		t.Errorf("an unstated scope is %q", got)
	}
	if _, err := ScopeFlags("everywhere"); err == nil {
		t.Error("an invented scope was accepted")
	}
}

// Granting and revoking is the whole of what a state does, so it is
// checked against a real security descriptor rather than a model of one.
func TestGrantAndRevokeChangeWhatTheListSays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed.conf")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Everyone, because it is the one account that certainly exists,
	// resolves the same on every machine, and needs no domain.
	if err := Grant(path, "Everyone", "read", "this_folder_only", false); err != nil {
		t.Fatalf("granting: %v", err)
	}
	got := findEntry(t, path, "Everyone")
	if got == nil {
		t.Fatal("the grant did not appear in the list")
	}
	if got.Permission != "read" {
		t.Errorf("granted %q, want read", got.Permission)
	}
	if got.Kind != "allow" {
		t.Errorf("the entry is %q", got.Kind)
	}
	if got.Inherited {
		t.Error("an entry written here is reported as inherited")
	}

	// Granting again replaces rather than adds. Two allow entries for
	// one account are the union of their masks, so a second grant of
	// `read` beside an existing `full_control` would read as a
	// tightening and would not be one.
	if err := Grant(path, "Everyone", "full_control", "this_folder_only", false); err != nil {
		t.Fatal(err)
	}
	if err := Grant(path, "Everyone", "read", "this_folder_only", false); err != nil {
		t.Fatal(err)
	}
	if n := countEntries(t, path, "Everyone"); n != 1 {
		t.Errorf("%d entries for one account; a grant must replace, not add", n)
	}
	if got := findEntry(t, path, "Everyone"); got == nil || got.Permission != "read" {
		t.Errorf("after regranting read, the entry is %+v", got)
	}

	// A deny entry is a different kind, not a different mask.
	if err := Grant(path, "Everyone", "write", "this_folder_only", true); err != nil {
		t.Fatal(err)
	}
	if got := findEntry(t, path, "Everyone"); got == nil || got.Kind != "deny" {
		t.Errorf("the deny entry is %+v", got)
	}

	if err := Revoke(path, "Everyone"); err != nil {
		t.Fatalf("revoking: %v", err)
	}
	if got := findEntry(t, path, "Everyone"); got != nil {
		t.Errorf("after revoking, the list still holds %+v", got)
	}

	// Revoking what is not there is not an error: a state asking for an
	// account to have no access is satisfied by one that has none.
	if err := Revoke(path, "Everyone"); err != nil {
		t.Errorf("revoking an account with no entry reported %v", err)
	}
}

// An account that does not exist is an error naming it. A grant to a
// misspelled account that appeared to work would grant nothing and say
// it had.
func TestGrantingToAnAccountThatIsNotThereSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Grant(path, "no-such-account-here", "read", "", false)
	if err == nil {
		t.Fatal("a grant to an account that does not exist succeeded")
	}
	if !strings.Contains(err.Error(), "no-such-account-here") {
		t.Errorf("the error does not name the account: %v", err)
	}
}

// Inheritance is the setting that decides whether the parent's entries
// reach a path, and turning it off has two forms that differ in whether
// what could be opened before can still be opened after.
func TestInheritanceCanBeTurnedOffKeepingOrDroppingWhatWasInherited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "child.conf")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A new file under a temporary directory inherits from it.
	on, err := Inherits(path)
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Fatal("a newly created file is not inheriting; this test cannot say anything")
	}
	inheritedBefore := 0
	for _, e := range entries(t, path) {
		if e.Inherited {
			inheritedBefore++
		}
	}
	if inheritedBefore == 0 {
		t.Fatal("nothing was inherited, so turning inheritance off proves nothing")
	}

	// Keeping them: the same access, now owned by this path.
	if err := SetInheritance(path, false, true); err != nil {
		t.Fatal(err)
	}
	if on, _ := Inherits(path); on {
		t.Error("inheritance is still on after turning it off")
	}
	kept := entries(t, path)
	if len(kept) < inheritedBefore {
		t.Errorf("keeping the inherited entries left %d of %d", len(kept), inheritedBefore)
	}
	for _, e := range kept {
		if e.Inherited {
			t.Errorf("an entry is still marked inherited after the copy: %+v", e)
		}
	}

	// And back on again.
	if err := SetInheritance(path, true, false); err != nil {
		t.Fatal(err)
	}
	if on, _ := Inherits(path); !on {
		t.Error("inheritance did not come back on")
	}
}

// Dropping the inherited entries is the other form, and it is the one
// that can leave a path nobody can open. It has to actually drop them.
func TestTurningInheritanceOffCanDropWhatWasInherited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "locked.conf")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// One entry of its own, so the file does not become unreadable to
	// the account running the test.
	if err := Restrict(path); err != nil {
		t.Fatal(err)
	}
	if err := SetInheritance(path, true, false); err != nil {
		t.Fatal(err)
	}
	before := len(entries(t, path))

	if err := SetInheritance(path, false, false); err != nil {
		t.Fatal(err)
	}
	after := entries(t, path)
	if len(after) >= before {
		t.Errorf("dropping the inherited entries left %d of %d", len(after), before)
	}
	for _, e := range after {
		if e.Inherited {
			t.Errorf("an inherited entry survived: %+v", e)
		}
	}
	// The file is still the owner's own.
	if _, err := os.ReadFile(path); err != nil {
		t.Errorf("the owner can no longer read it: %v", err)
	}
}

// The owner can be read back after it is set. Setting it to the account
// already running needs no privilege, which is the case this can check
// on any machine.
func TestSettingTheOwnerToTheCurrentAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	me, err := Owner(path)
	if err != nil {
		t.Fatal(err)
	}
	if me == "" {
		t.Fatal("the file has no owner")
	}
	if err := SetOwner(path, me); err != nil {
		t.Fatalf("setting the owner to the account that already owns it: %v", err)
	}
	got, err := Owner(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != me {
		t.Errorf("owner = %q, want %q", got, me)
	}
}

func entries(t *testing.T, path string) []ACE {
	t.Helper()
	got, err := Entries(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func findEntry(t *testing.T, path, trustee string) *ACE {
	t.Helper()
	for _, e := range entries(t, path) {
		if strings.EqualFold(e.Trustee, trustee) {
			return &e
		}
	}
	return nil
}

func countEntries(t *testing.T, path, trustee string) int {
	t.Helper()
	n := 0
	for _, e := range entries(t, path) {
		if strings.EqualFold(e.Trustee, trustee) {
			n++
		}
	}
	return n
}

// An empty list must deny everyone, not grant everyone.
//
// This is the sharpest edge in the file. A DACL that is absent grants
// Everyone full control; a DACL that is present and empty grants nobody
// anything. windows.ACLFromEntries with no entries returns NULL, and
// passing that on would set the first while the caller asked for the
// second — silently, on whatever path a state was trying to lock down.
func TestRevokingTheLastEntryDeniesEveryoneRatherThanGrantingThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Inheritance off and one entry of our own, so revoking it leaves
	// the list genuinely empty rather than falling back to the parent's.
	if err := Grant(path, "Everyone", "full_control", "this_folder_only", false); err != nil {
		t.Fatal(err)
	}
	if err := SetInheritance(path, false, false); err != nil {
		t.Fatal(err)
	}
	if err := Revoke(path, "Everyone"); err != nil {
		t.Fatal(err)
	}

	got := entries(t, path)
	if len(got) != 0 {
		t.Fatalf("the list should be empty, and holds %+v", got)
	}
	// Entries reports a NULL list as an explicit Everyone/full_control
	// entry, so an empty answer here is itself the assertion: had the
	// NULL been written, this would have read one entry granting the
	// world everything.
	for _, e := range got {
		if e.Trustee == "Everyone" {
			t.Errorf("an empty list was written as a NULL one, granting %+v", e)
		}
	}
}
