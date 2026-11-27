package cli

import "testing"

// The parser reads `-h` as a flag called `h`, so a program that asked only
// whether `help` was set ran the command instead of describing it (issue 882).
func TestHelpIsAskedForByEitherSpelling(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"--help"}, true},
		{[]string{"-h"}, true},
		{[]string{"-h", "now"}, true},
		{[]string{"--help=false"}, false},
		{[]string{"--hub", "x"}, false},
		{nil, false},
	} {
		args, err := Parse(tc.argv)
		if err != nil {
			t.Fatal(err)
		}
		if got := args.Help(); got != tc.want {
			t.Errorf("Help() for %q = %v, want %v", tc.argv, got, tc.want)
		}
	}
}
