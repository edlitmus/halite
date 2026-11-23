package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/treesign"
)

// A plan's window holds the whole orchestration, and a node refuses a
// signed job further from expiry than its job_signature_max_ttl. So a
// --ttl over the nodes' cap is refused, and the refusal says the two ways
// on and what the second one costs.
func TestAPlanLongerThanTheNodesAcceptIsRefusedNamingTheWaysOn(t *testing.T) {
	if err := checkPlanTTL(time.Hour, time.Hour); err != nil {
		t.Errorf("an hour against the default cap: %v", err)
	}
	if err := checkPlanTTL(4*time.Hour, 6*time.Hour); err != nil {
		t.Errorf("four hours against nodes raised to six: %v", err)
	}

	err := checkPlanTTL(2*time.Hour, time.Hour)

	if err == nil {
		t.Fatal("a two-hour plan against an hour's cap was accepted")
	}
	for _, want := range []string{"Split a long orchestration into plans", "raise job_signature_max_ttl",
		"--max-ttl", "lengthens the life of every signed job"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// The plan records the issue 764 revision of the checkout only when the
// checkout is what its manifest says: an id recorded beside files that
// moved on would claim a binding that is not there.
func TestAPlanRecordsTheCheckoutsRevisionOnlyWhenItMatches(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("deploy.sls", "x:\n  salt.function:\n    - name: test.ping\n    - tgt: '*'\n")
	roots := fileserver.NewRoots(map[string][]string{"base": {dir}})
	if got, err := checkoutRevision(roots, "base"); err != nil || got != "" {
		t.Fatalf("a checkout with no manifest: %q %v", got, err)
	}
	files, err := roots.TreeEntries("base", "sha256")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := treesign.Encode(&treesign.Manifest{Env: "base", Issued: treesign.FormatTime(time.Now()),
		HashType: "sha256", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	write(treesign.ManifestPath, string(raw))

	got, err := checkoutRevision(roots, "base")

	if err != nil || got != treesign.ID(raw) {
		t.Fatalf("a matching checkout: %q %v, want %s", got, err, treesign.ID(raw))
	}
	write("deploy.sls", "changed: {}\n")
	if _, err := checkoutRevision(roots, "base"); err == nil || !strings.Contains(err.Error(), "deploy.sls differs") {
		t.Errorf("a checkout whose file moved on: %v", err)
	}
	if _, err := checkoutRevision(roots, "prod"); err == nil {
		t.Error("a manifest for base was recorded for prod")
	}
}
