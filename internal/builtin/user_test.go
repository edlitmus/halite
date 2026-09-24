package builtin

import (
	"strings"
	"testing"
)

// What `groups` means, as the set usermod -G and pw usermod -G are
// handed. Both replace the whole supplementary list, so the set has to
// be complete: without remove_groups it keeps every membership the
// account already has (DIVERGENCE 5.135).
func TestWantedGroupsKeepsWhatItWasNotAskedToRemove(t *testing.T) {
	for _, tc := range []struct {
		name         string
		have, want   []string
		remove       bool
		final        string
		changeNeeded bool
	}{
		{"nothing missing, nothing to remove", []string{"a", "b", "hand"}, []string{"a", "b"}, false, "a|b|hand", false},
		{"one missing keeps the hand-added one", []string{"a", "hand"}, []string{"a", "b"}, false, "a|b|hand", true},
		{"a dropped group stays by default", []string{"a", "b"}, []string{"a"}, false, "a|b", false},
		{"remove_groups drops what is not named", []string{"a", "b", "hand"}, []string{"a"}, true, "a", true},
		{"remove_groups already exact", []string{"a"}, []string{"a"}, true, "a", false},
		{"remove_groups adds and drops together", []string{"a", "hand"}, []string{"a", "b"}, true, "a|b", true},
	} {
		final, differs := wantedGroups(tc.have, tc.want, tc.remove)
		if strings.Join(final, "|") != tc.final || differs != tc.changeNeeded {
			t.Errorf("%s: got %v (differs=%v), want %s (differs=%v)",
				tc.name, final, differs, tc.final, tc.changeNeeded)
		}
	}
}

// remove_groups with nothing to keep would clear every supplementary
// group through a `-G ""` nobody has run, so it is refused by name.
func TestRemoveGroupsWithNoGroupsIsRefused(t *testing.T) {
	if why := removeGroupsUnsupported(userSpec{Name: "x", RemoveGroups: true}); why == "" {
		t.Error("remove_groups with no groups was accepted")
	}
	if why := removeGroupsUnsupported(userSpec{Name: "x", RemoveGroups: true, Groups: []string{"a"}}); why != "" {
		t.Errorf("remove_groups with groups was refused: %s", why)
	}
}
