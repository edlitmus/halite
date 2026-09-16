package main

import (
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// The hub's redactor is what `h.secrets.Add` is reached through when
// pillar is compiled and when an external source answers. A context
// built without one does not degrade: `Add` takes a pointer receiver
// and locks, so a nil set panics at the first secret the hub decrypts
// -- which is the first time it matters and the worst time to find out.
func TestTheHubContextCarriesARedactor(t *testing.T) {
	// A root with no configuration in it: AllowMissing means the hub
	// still builds, which is all this needs.
	args := &cli.Args{Flags: map[string]string{"root": t.TempDir()}}

	h := openHubForConfig(args)
	if h.secrets == nil {
		t.Fatal("openHubForConfig built a hub with no redactor")
	}
	// Usable, not merely non-nil.
	h.secrets.Add("a-decrypted-pillar-value")
	if got := h.secrets.Scrub("saw a-decrypted-pillar-value"); got == "saw a-decrypted-pillar-value" {
		t.Errorf("the redactor records nothing: %q", got)
	}
}
