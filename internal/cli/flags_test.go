package cli

import "testing"

const sampleUsage = `halite-hub policy — the RBAC of SPEC section 23.5

  policy test <principal> <target> <function> [args...]

policy flags:
  --runner             evaluate the function as a runner rather than a job
  --kwarg <k=v>        an argument to include, repeatable as a comma list
  --out <format>       summary (default) or json
  -v                   print the version
`

func TestFlagNamesReadsTheFlagsAUsageDescribes(t *testing.T) {
	names := FlagNames(sampleUsage)
	for _, want := range []string{"runner", "kwarg", "out", "v"} {
		if !names[want] {
			t.Errorf("usage documents %q and FlagNames missed it", want)
		}
	}
	// Prose and placeholders are not flags.
	for _, unwanted := range []string{"principal", "format", "policy", "k"} {
		if names[unwanted] {
			t.Errorf("FlagNames read %q out of prose as though it were a flag", unwanted)
		}
	}
}

func TestUnknownFlagsNamesOnlyWhatIsUndocumented(t *testing.T) {
	args, err := Parse([]string{"test", "--runner", "--out", "json", "--policy", "x.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	unknown := UnknownFlags(args, sampleUsage)
	if len(unknown) != 1 || unknown[0] != "policy" {
		t.Fatalf("expected only --policy to be unknown, got %v", unknown)
	}
}

// A flag that no usage repeats is still accepted everywhere, because
// every command answers --help and --version.
func TestCommonFlagsAreAlwaysKnown(t *testing.T) {
	args, err := Parse([]string{"--help", "--version"})
	if err != nil {
		t.Fatal(err)
	}
	if unknown := UnknownFlags(args, "a usage with no flags in it"); len(unknown) != 0 {
		t.Fatalf("--help and --version should never be unknown, got %v", unknown)
	}
}

func TestClosestSuggestsTheFlagThatWasMeant(t *testing.T) {
	known := FlagNames(sampleUsage)
	for _, tc := range []struct{ typed, want string }{
		{"runer", "runner"},
		{"kwrag", "kwarg"},
		{"ou", "out"},
		{"completely-different", ""},
	} {
		if got := closest(tc.typed, known); got != tc.want {
			t.Errorf("closest(%q) = %q, want %q", tc.typed, got, tc.want)
		}
	}
}

// The bug this was written for: `policy test --policy other.yaml` read
// as a request to evaluate that file and silently evaluated the
// configured one instead.
func TestAnIgnoredFlagIsReportedRatherThanDropped(t *testing.T) {
	args, err := Parse([]string{"test", "cert:CN=ed", "*", "state.apply", "--policy", "other.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if got := args.Flag("policy", ""); got != "other.yaml" {
		t.Fatalf("the parser should still hold the flag, got %q", got)
	}
	unknown := UnknownFlags(args, sampleUsage)
	if len(unknown) != 1 || unknown[0] != "policy" {
		t.Fatalf("the undocumented flag should be reported, got %v", unknown)
	}
}
