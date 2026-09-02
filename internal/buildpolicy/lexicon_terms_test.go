package buildpolicy

import "testing"

// The prohibited terms are caught wherever they stand as a word, and
// only permitted inside a longer one.
//
// `\b` alone was not enough: an underscore is a word character, so
// `halite_master` — the old name of a file this project has since
// renamed — went unreported while `master_port` was caught. A term is no
// less prohibited for having something in front of it.
func TestATermIsCaughtInEitherPosition(t *testing.T) {
	for _, tc := range []struct {
		text   string
		banned bool
	}{
		{"halite_master", true},
		{"master_port", true},
		{"salt-master", true},
		{"MasterKey", true},
		{"the master decides", true},
		{"is_minion", true},
		{"minion_id", true},
		{"role.master", true},
		{"state_whitelist", true},

		// Longer words that merely contain the letters are not the term.
		{"mastered the subject", false},
		{"a masterless node", false},
		{"remastering", false},
		{"dominion", false},
	} {
		var hit bool
		for _, term := range Terms {
			if term.MatchString(tc.text) {
				hit = true
				break
			}
		}
		if hit != tc.banned {
			verb := "should not have"
			if tc.banned {
				verb = "should have"
			}
			t.Errorf("%q %s matched a prohibited term", tc.text, verb)
		}
	}
}
