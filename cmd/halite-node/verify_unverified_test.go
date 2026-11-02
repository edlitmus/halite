package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/nodeevidence"
)

// `verify-evidence` does not say a chain holds, and does not exit 0, while a
// record in it could not be checked: an edited record given a schema this
// build does not know passed as "a newer halite, not a broken chain".
// DIVERGENCE 5.543.
func TestVerifyEvidenceDoesNotPassARecordItCouldNotCheck(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, "state_dir: "+filepath.Join(root, "state")+"\nnode_id: web1.example\n")
	dir := filepath.Join(root, "state", "evidence")
	l, err := nodeevidence.Open(dir, nodeevidence.Options{NodeID: "web1.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, fun := range []string{"alpha", "bravo"} {
		if err := l.Append(nodeevidence.KindJobAccepted, map[string]string{"fun": fun}); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	common := []string{"--root", root, "--config", cfg}

	if got := run(t, append([]string{"verify-evidence"}, common...)...); got.code != 0 ||
		!strings.Contains(got.stdout, "The chain holds") {
		t.Fatalf("an untouched chain: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	segments, _ := nodeevidence.Segments(dir)
	raw, err := os.ReadFile(segments[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	lines[1] = strings.Replace(lines[1], `"fun":"bravo"`, `"fun":"cmd.run rm -rf"`, 1)
	lines[1] = strings.Replace(lines[1], `"schema":"halite.evidence/1"`, `"schema":"halite.evidence/99"`, 1)
	if err := os.WriteFile(segments[0], []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	got := run(t, append([]string{"verify-evidence"}, common...)...)
	if got.code == 0 || strings.Contains(got.stdout, "The chain holds") {
		t.Errorf("a chain with a record it could not check passed: exit %d\n%s", got.code, got.stdout)
	}
	if !strings.Contains(got.stdout, "cannot tell") {
		t.Errorf("the output does not say what it could not establish:\n%s", got.stdout)
	}
}
