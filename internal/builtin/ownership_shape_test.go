package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

// plannedOwnership reports one shape whether or not the file is already
// there. Every caller nests the result under "ownership", so a wrapper
// added here produced changes.ownership.ownership for a file that did not
// exist yet -- the same state describing the same change two ways
// depending on a detail of the filesystem.
func TestPlannedOwnershipHasOneShape(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "there")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "not-there")

	// root is the one owner every test machine has, so ask for a change
	// away from it on a file root owns.
	for _, tc := range []struct {
		name   string
		path   string
		exists bool
	}{
		{"a file that exists", existing, true},
		{"a file that does not", missing, false},
	} {
		change, differs, err := plannedOwnership(tc.path, tc.exists, "nobody", "")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !differs || change == nil {
			t.Fatalf("%s: no change was planned", tc.name)
		}
		if _, nested := change.Get("ownership"); nested {
			t.Errorf("%s: the change is wrapped in another \"ownership\" key; "+
				"callers nest it themselves, so this becomes ownership.ownership", tc.name)
		}
		if _, ok := change.Get("new"); !ok {
			t.Errorf("%s: the change has no \"new\"; it is %v", tc.name, change.StringKeys())
		}
	}
}
