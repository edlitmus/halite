package value

import (
	"strings"
	"testing"
	"time"
)

// Both spellings a tree writes, read the same way by every caller.
//
// The two parsers this replaces disagreed about exactly one of these: the
// quoted bare number, which is what a template produces for every
// duration however the pillar spelled it. DIVERGENCE 5.137.
func TestParseDurationTakesBothSpellingsATreeWrites(t *testing.T) {
	cases := map[any]time.Duration{
		"15m":           15 * time.Minute,
		"2h30m":         2*time.Hour + 30*time.Minute,
		"90s":           90 * time.Second,
		"900":           900 * time.Second,
		"0.5":           500 * time.Millisecond,
		int64(900):      900 * time.Second,
		int(30):         30 * time.Second,
		float64(1.5):    1500 * time.Millisecond,
		5 * time.Second: 5 * time.Second,
	}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil {
			t.Errorf("%#v: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%#v -> %v, want %v", in, got, want)
		}
	}
}

// And a value nobody can read is an error, not a zero.
//
// A zero is what the caller that swallowed this turned into "no deadline
// at all", so the distinction is the whole point: a duration that cannot
// be read must stop the caller rather than quietly mean nothing.
func TestParseDurationRefusesWhatItCannotRead(t *testing.T) {
	for _, in := range []any{"soon", "", "15 minutes", true, nil, []any{"15m"}} {
		got, err := ParseDuration(in)
		if err == nil {
			t.Errorf("%#v was accepted as %v", in, got)
			continue
		}
		if got != 0 {
			t.Errorf("%#v failed and still returned %v", in, got)
		}
	}
	// The message says how to write one, because the operator reading it
	// has a word in a file and needs to know what to put there.
	_, err := ParseDuration("soon")
	if !strings.Contains(err.Error(), "number of seconds") {
		t.Errorf("the refusal does not say what to write: %v", err)
	}
}
