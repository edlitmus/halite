package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
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
		// It also has to describe the change. Where that lives is the
		// platform's business: unix reports the pair directly, Windows
		// reports it per attribute as {"user": {old, new}}. Both nest
		// correctly under the caller's "ownership"; asserting the unix
		// payload here made this fail on Windows, which was the test
		// being parochial rather than the implementation being wrong.
		if !describesAChange(change) {
			t.Errorf("%s: the change says nothing; it is %v", tc.name, change.StringKeys())
		}
	}
}

// describesAChange reports whether a planned change carries an old/new
// pair, either directly or one level down.
func describesAChange(change *value.Map) bool {
	if _, ok := change.Get("new"); ok {
		return true
	}
	for _, e := range change.Entries() {
		if inner, ok := e.Val.(*value.Map); ok {
			if _, ok := inner.Get("new"); ok {
				return true
			}
		}
	}
	return false
}

// Both platforms' shapes are asserted on every platform, because the
// original version of the test above asserted only the one it ran on and
// passed everywhere it was run locally -- then failed on Windows in CI,
// where the payload is per-attribute rather than a bare pair. A
// cross-platform invariant that is only ever checked on one platform is
// not a cross-platform invariant.
func TestBothOwnershipShapesDescribeAChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change *value.Map
		want   bool
	}{
		{
			name:   "the unix shape, a bare old/new pair",
			change: states.Change("0:0", "nobody"),
			want:   true,
		},
		{
			name:   "the Windows shape, one pair per attribute",
			change: value.MapOf("user", states.Change("", "nobody")),
			want:   true,
		},
		{
			name:   "a map that says nothing",
			change: value.MapOf("user", "nobody"),
			want:   false,
		},
		{
			name:   "empty",
			change: value.NewMap(0),
			want:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := describesAChange(tc.change); got != tc.want {
				t.Errorf("describesAChange = %v, want %v", got, tc.want)
			}
			if _, nested := tc.change.Get("ownership"); nested {
				t.Error("the fixture wraps itself in \"ownership\"")
			}
		})
	}
}
