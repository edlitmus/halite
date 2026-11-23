package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/treesign"
)

// A node's own roots under `state_signature_local`, through setup and
// executeJob: the wiring, not just the guard.
func signedLocalNode(t *testing.T, extra string) (*node, string, func(dir string)) {
	t.Helper()
	dir := t.TempDir()
	key, err := jobsign.GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	line, _ := jobsign.FormatSignerKey("tree-ci", &key.PublicKey)
	states, devStates, pillars := filepath.Join(dir, "states"), filepath.Join(dir, "dev"), filepath.Join(dir, "pillar")
	for path, body := range map[string]string{
		filepath.Join(states, "top.sls"):     "base:\n  '*':\n    - web\n",
		filepath.Join(states, "web.sls"):     "say:\n  test.nop: []\n",
		filepath.Join(devStates, "top.sls"):  "dev:\n  '*':\n    - web\n",
		filepath.Join(devStates, "web.sls"):  "say_dev:\n  test.nop: []\n",
		filepath.Join(pillars, "top.sls"):    "base: {}\n",
		filepath.Join(pillars, "devtop.sls"): "dev: {}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := writeConfig(t, dir, "node_id: web1.example\nevidence: false\n"+
		"state_dir: "+filepath.Join(dir, "state")+"\ncache_dir: "+filepath.Join(dir, "cache")+"\n"+
		"file_roots: {base: ["+states+"], dev: ["+devStates+"]}\npillar_roots: {base: ["+pillars+"], dev: ["+pillars+"]}\n"+
		"require_state_signature: true\nstate_signer_keys: ['"+line+"']\n"+extra)
	args, err := cli.Parse([]string{"--config", path, "--root", dir})
	if err != nil {
		t.Fatal(err)
	}
	n := setup(args)
	sign := func(root string) {
		t.Helper()
		env := "base"
		if root == devStates {
			env = "dev"
		}
		roots := fileserver.NewRoots(map[string][]string{env: {root}})
		entries, err := roots.TreeEntries(env, "sha256")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := treesign.Encode(&treesign.Manifest{Env: env, Issued: treesign.FormatTime(time.Now()),
			HashType: "sha256", Files: entries})
		if err != nil {
			t.Fatal(err)
		}
		sig, _ := treesign.SignatureLine(key, raw)
		if err := os.MkdirAll(filepath.Join(root, "_halite"), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(root, treesign.ManifestPath), raw, 0o600)
		_ = os.WriteFile(filepath.Join(root, treesign.SignaturePath), []byte(sig+"\n"), 0o600)
	}
	return n, dir, sign
}

func showLowstate(n *node, env string) *job.Return {
	return n.executeJob(&job.Job{JID: job.ID("20261122T1"), Fun: "state.show_lowstate", Env: env})
}

func TestTheNodesOwnRootsAreHeldToTheRequirement(t *testing.T) {
	n, dir, sign := signedLocalNode(t, "")
	ret := showLowstate(n, "")
	if ret.Success || !strings.Contains(string(ret.Return), "serves no signed manifest") {
		t.Fatalf("an unsigned local tree: %s", ret.Return)
	}
	// A highstate reads every environment's top file, and each is held to
	// its own manifest: signing base alone is not enough.
	sign(filepath.Join(dir, "states"))
	ret = showLowstate(n, "")
	if ret.Success || !strings.Contains(string(ret.Return), "environment dev") {
		t.Fatalf("base signed, dev not: %s", ret.Return)
	}
	sign(filepath.Join(dir, "dev"))
	if ret := showLowstate(n, ""); !ret.Success {
		t.Fatalf("both signed: %s", ret.Return)
	}
	// A job naming another environment rebuilds the local tree; the
	// rebuilt one is held to the same rule.
	if err := os.WriteFile(filepath.Join(dir, "dev", "web.sls"), []byte("rm:\n  cmd.run: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ret = showLowstate(n, "dev")
	if ret.Success || !strings.Contains(string(ret.Return), "it was not used") {
		t.Fatalf("a changed file in a job for dev: %s", ret.Return)
	}
}

func TestStateSignatureLocalFalseLeavesTheOwnRootsAlone(t *testing.T) {
	n, _, _ := signedLocalNode(t, "state_signature_local: false\n")
	if ret := showLowstate(n, ""); !ret.Success {
		t.Fatalf("got %s", ret.Return)
	}
	if !strings.Contains(n.treeSignatureSummary(), "the hub's tree only") {
		t.Errorf("the summary: %s", n.treeSignatureSummary())
	}
}

// Every refusal reason is a series from the start, so the first refusal
// is an increase rather than a series being born; and the accepted
// revision's issued time is what an alert on age reads.
func TestTreeRefusalsAreExportedAtZeroAndTheRevisionAsATime(t *testing.T) {
	m := nodeMetricsFor(t, "metrics_listen: 127.0.0.1:0\n")
	m.declareTreeRefusals()
	issued := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	m.treeRevision("base", issued)
	got := expositionOf(t, m)
	for _, reason := range treesign.Reasons {
		if !strings.Contains(got, `halite_node_tree_refusals_total{reason="`+string(reason)+`"} 0`) {
			t.Errorf("no zero series for %s", reason)
		}
	}
	if !strings.Contains(got, `halite_node_tree_revision_issued_timestamp_seconds{env="base"} 1.7953056e+09`) {
		t.Errorf("the issued time:\n%s", got)
	}
}

func TestTheFreezeBoundIsOnByDefault(t *testing.T) {
	n, _, _ := signedLocalNode(t, "")
	if n.treeGuard.MaxAge != DefaultStateManifestMaxAge || DefaultStateManifestMaxAge != 30*24*time.Hour {
		t.Fatalf("the bound is %s", n.treeGuard.MaxAge)
	}
	n, _, _ = signedLocalNode(t, "state_manifest_max_age: 0\n")
	if n.treeGuard.MaxAge != 0 {
		t.Fatalf("0 left the bound at %s", n.treeGuard.MaxAge)
	}
}
