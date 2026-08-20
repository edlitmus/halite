package grains

import (
	"os"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// TestDumpGrains prints what this host actually reports, so that a change
// to collection can be eyeballed against a real machine rather than only
// against a fixture. It asserts nothing beyond collection succeeding.
func TestDumpGrains(t *testing.T) {
	if os.Getenv("HALITE_DUMP_GRAINS") == "" {
		t.Skip("set HALITE_DUMP_GRAINS=1 to print this host's grains")
	}
	g, warns := Collect(Options{NodeID: "dump.test"})
	for _, e := range g.Entries() {
		b, _ := value.EncodeJSON(e.Val, 0)
		t.Logf("%-22s %s", value.KeyString(e.Key), b)
	}
	t.Logf("total grains: %d, warnings: %v", g.Len(), warns)
}
