package extconform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The extensions this project ships have to pass their own harness.
//
// Both are documentation as much as code: `cmd/halite-ext-aws-secrets`
// is what `docs/extensions.md` walks through, and the Python one exists
// to show the protocol needs no Go. An example that has quietly stopped
// conforming teaches the wrong thing to everyone who copies it, and
// there is no way to notice by reading.
//
// This is also the check that keeps the protocol honest in the other
// direction. A change to the wire that the host and `ext` agree on
// between themselves will break the hand-written Python here, which is
// the only implementation in the tree that cannot be fixed by the same
// edit.

func verifyShipped(t *testing.T, path, kind string, kwargs any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	results, err := Run(ctx, Options{
		Path: path, Kind: kind, Kwargs: kwargs, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("the harness could not run: %v", err)
	}
	for _, r := range results {
		switch r.Status {
		case Fail:
			t.Errorf("%s: %s\n      %s", r.Rule, r.Title, r.Detail)
		case Skip:
			// A skip here means the harness could not establish
			// something about an extension in this tree, which is worth
			// knowing even though it is not a failure.
			t.Logf("skipped %s: %s", r.Rule, r.Detail)
		}
	}
	pass, fail, skip := Counts(results)
	t.Logf("%d pass, %d fail, %d skip", pass, fail, skip)
	if pass < 10 {
		t.Errorf("only %d rules passed; the harness checked almost nothing", pass)
	}
}

func TestTheGoExtensionConforms(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	dir := t.TempDir()
	name := "aws-secrets"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", path, "../../cmd/halite-ext-aws-secrets")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building it: %v", err)
	}
	// An empty block, so the call fails for want of a secret rather
	// than reaching AWS. What is being checked is the shape of the
	// answer, and a failure is as good a shape as a success.
	verifyShipped(t, path, "pillar", map[string]any{"node_id": "conformance"})
}

func TestThePythonExampleConforms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shebang is not how Windows starts a program")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on this machine")
	}
	path, err := filepath.Abs("../../contrib/extensions/python/example_pillar.py")
	if err != nil {
		t.Fatal(err)
	}
	verifyShipped(t, path, "pillar", map[string]any{"node_id": "conformance"})
}
