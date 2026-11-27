package main

import (
	"strings"
	"testing"
)

// defaultHubAddress is the host of `listen`, and localhost only for a
// wildcard (DIVERGENCE 5.725). The usage text said "default localhost" on
// three commands after that changed, which sent an operator with
// `listen: 10.0.0.5:4510` to a hub that was not listening there (issue 884).
func TestUsageSaysWhereAnOperatorCommandReachesTheHubByDefault(t *testing.T) {
	if got := defaultHubAddress("10.0.0.5:4510"); got != "10.0.0.5" {
		t.Fatalf("defaultHubAddress(10.0.0.5:4510) = %q; this test describes a rule the code no longer has", got)
	}

	checked := 0
	for _, text := range strings.Split(usage, "\n") {
		if !strings.Contains(text, "--hub <address>") {
			continue
		}
		checked++
		if strings.Contains(text, "default localhost") {
			t.Errorf("the usage says the hub address defaults to localhost: %q", strings.TrimSpace(text))
		}
	}
	if checked == 0 {
		t.Error("no --hub line was found in the usage text; this test has stopped checking anything")
	}
}
