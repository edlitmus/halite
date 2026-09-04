package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/winsec"
)

// win_dacl.present declares what an account has, and a state that
// declares something has to converge on it: apply, then apply again and
// report nothing.
func TestWinDACLPresentGrantsAndThenConverges(t *testing.T) {
	r := New()
	path := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := value.MapOf("name", path, "trustee", "Everyone",
		"permission", "read", "applies_to", "this_folder_only")

	// Test mode predicts the change and makes none.
	predicted, err := r.States.Call(newCtx(true), "win_dacl.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if predicted.Result != nil {
		t.Errorf("test mode should predict, not decide: %+v", predicted)
	}
	if predicted.Changes.Len() == 0 {
		t.Error("test mode predicted no change on a file with no such entry")
	}
	if got := entryFor(t, path, "Everyone"); got != nil {
		t.Fatalf("test mode changed the list: %+v", got)
	}

	first, err := r.States.Call(newCtx(false), "win_dacl.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if first.Result == nil || !*first.Result {
		t.Fatalf("the grant failed: %+v", first)
	}
	if first.Changes.Len() == 0 {
		t.Error("a grant that changed the list reported no changes")
	}
	got := entryFor(t, path, "Everyone")
	if got == nil || got.Permission != "read" {
		t.Fatalf("the entry is %+v", got)
	}

	second, err := r.States.Call(newCtx(false), "win_dacl.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changes.Len() != 0 {
		t.Errorf("the second run reported changes: %+v", second.Changes)
	}
	if !strings.Contains(second.Comment, "already") {
		t.Errorf("comment = %q", second.Comment)
	}
}

// A declaration is exact. A state that says an account has `read` and
// leaves a `full_control` entry in place because it satisfies the read
// has not done what the file says.
func TestWinDACLPresentTightensAnEntryThatIsTooWide(t *testing.T) {
	r := New()
	path := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := winsec.Grant(path, "Everyone", "full_control", "this_folder_only", false); err != nil {
		t.Fatal(err)
	}

	res, err := r.States.Call(newCtx(false), "win_dacl.present",
		value.MapOf("name", path, "trustee", "Everyone",
			"permission", "read", "applies_to", "this_folder_only"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changes.Len() == 0 {
		t.Fatal("full_control was left in place for a state declaring read")
	}
	if got := entryFor(t, path, "Everyone"); got == nil || got.Permission != "read" {
		t.Errorf("the entry is %+v", got)
	}
}

func TestWinDACLAbsentRemovesAnEntryAndThenConverges(t *testing.T) {
	r := New()
	path := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := winsec.Grant(path, "Everyone", "read", "this_folder_only", false); err != nil {
		t.Fatal(err)
	}
	args := value.MapOf("name", path, "trustee", "Everyone")

	first, err := r.States.Call(newCtx(false), "win_dacl.absent", args)
	if err != nil {
		t.Fatal(err)
	}
	if first.Changes.Len() == 0 {
		t.Error("removing an entry reported no change")
	}
	if got := entryFor(t, path, "Everyone"); got != nil {
		t.Errorf("the entry survived: %+v", got)
	}

	second, err := r.States.Call(newCtx(false), "win_dacl.absent", args)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changes.Len() != 0 {
		t.Errorf("the second run reported changes: %+v", second.Changes)
	}
}

// A file state's `user:` was refused on this platform, so no tree could
// manage ownership on Windows at all. It now sets the owner, and — the
// part that matters for a highstate — reports nothing on the second run.
func TestAFileStateSetsAnOwnerAndConverges(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "owned.conf")

	me, err := winsec.Owner(dir)
	if err != nil {
		t.Fatal(err)
	}
	args := value.MapOf("name", path, "contents", "hello\n", "user", me)

	first, err := r.States.Call(newCtx(false), "file.managed", args)
	if err != nil {
		t.Fatal(err)
	}
	if first.Result == nil || !*first.Result {
		t.Fatalf("the state failed: %+v", first)
	}
	owner, err := winsec.Owner(path)
	if err != nil {
		t.Fatal(err)
	}
	if !sameAccount(owner, me) {
		t.Errorf("owner = %q, want %q", owner, me)
	}

	second, err := r.States.Call(newCtx(false), "file.managed", args)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changes.Len() != 0 {
		t.Errorf("the second run reported changes: %+v", second.Changes)
	}
}

// `group:` is still refused, because a file here has no group. It is
// refused by name rather than mapped onto the primary group, which
// nothing on the platform reads: a state that passed while granting
// nobody anything would be worse than one that fails.
func TestAFileStateRefusesAGroupAndSaysWhy(t *testing.T) {
	r := New()
	path := filepath.Join(t.TempDir(), "g.conf")

	res, err := r.States.Call(newCtx(false), "file.managed",
		value.MapOf("name", path, "contents", "x", "group", "Administrators"))
	if err == nil && res.Result != nil && *res.Result {
		t.Fatal("a group was accepted on Windows")
	}
	text := res.Comment
	if err != nil {
		text = err.Error()
	}
	if !strings.Contains(text, "win_dacl") {
		t.Errorf("the refusal does not point at what to use instead: %s", text)
	}
	if !strings.Contains(text, "Administrators") {
		t.Errorf("the refusal does not name the group asked for: %s", text)
	}
}

// An account name is compared the way Windows compares one. A bare name
// and its qualified form are one account, and a state that reported a
// change between them would never converge.
func TestAccountNamesAreComparedTheWayWindowsDoes(t *testing.T) {
	for _, c := range []struct{ a, b string }{
		{"Administrators", `BUILTIN\Administrators`},
		{`BUILTIN\Administrators`, "administrators"},
		{"Everyone", "EVERYONE"},
	} {
		if !sameAccount(c.a, c.b) {
			t.Errorf("%q and %q should be one account", c.a, c.b)
		}
	}
	if sameAccount("Administrators", "Users") {
		t.Error("two different groups compared equal")
	}
}

// has_permission answers "at least" by default, because that is the
// question an onlyif asks, and exactly on request, because that is the
// question a comparison asks.
func TestHasPermissionAnswersAtLeastAndExactly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.conf")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := winsec.Grant(path, "Everyone", "full_control", "this_folder_only", false); err != nil {
		t.Fatal(err)
	}

	atLeast, err := winDACLHasPermission(path, "Everyone", "read", false)
	if err != nil {
		t.Fatal(err)
	}
	if !atLeast {
		t.Error("an account with full_control does have read")
	}
	exact, err := winDACLHasPermission(path, "Everyone", "read", true)
	if err != nil {
		t.Fatal(err)
	}
	if exact {
		t.Error("full_control is not exactly read")
	}

	// A deny entry never counts as having the permission, whatever its
	// mask: Windows evaluates deny first.
	if err := winsec.Grant(path, "Everyone", "full_control", "this_folder_only", true); err != nil {
		t.Fatal(err)
	}
	denied, err := winDACLHasPermission(path, "Everyone", "read", false)
	if err != nil {
		t.Fatal(err)
	}
	if denied {
		t.Error("an account with a deny entry was reported as having the access")
	}
}

func entryFor(t *testing.T, path, trustee string) *winsec.ACE {
	t.Helper()
	entries, err := winsec.Entries(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Inherited || !sameAccount(e.Trustee, trustee) {
			continue
		}
		return &e
	}
	return nil
}

var _ = states.Change
