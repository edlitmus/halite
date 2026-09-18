//go:build unix

package exec

import "testing"

// macOS will not take more supplementary groups than NGROUPS_MAX, and
// the failure when it is handed more says nothing about groups.
//
// `fork/exec /usr/bin/defaults: invalid argument` is what came back --
// naming a binary that is present, executable and innocent. A CI
// account is in dozens of groups where a personal one is in a handful,
// so this was reachable only from a machine nobody had run it on.
func TestSupplementaryGroupsAreCappedOnDarwinAlone(t *testing.T) {
	many := make([]uint32, 40)
	for i := range many {
		many[i] = uint32(i + 20)
	}

	got := capGroups("darwin", many)
	if len(got) != darwinMaxGroups {
		t.Errorf("darwin kept %d groups, which setgroups refuses; want %d",
			len(got), darwinMaxGroups)
	}
	// The primary group leads the list and has to survive the trim, or
	// the child runs without the group it was given.
	if len(got) > 0 && got[0] != many[0] {
		t.Errorf("the first group became %d, want %d", got[0], many[0])
	}

	for _, goos := range []string{"linux", "freebsd", "openbsd", "netbsd", "illumos"} {
		if n := len(capGroups(goos, many)); n != len(many) {
			t.Errorf("%s kept %d of %d groups; the cap is macOS's alone and "+
				"dropping them anywhere else removes access the account has",
				goos, n, len(many))
		}
	}

	// A list already within the cap is handed back untouched, including
	// on darwin -- the common case, and the one a personal Mac hits.
	few := []uint32{20, 21, 22}
	if got := capGroups("darwin", few); len(got) != len(few) {
		t.Errorf("a short list was trimmed to %d", len(got))
	}
	if capGroups("darwin", nil) != nil {
		t.Error("a nil group list did not come back nil")
	}
}
