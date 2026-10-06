package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

func evidenceRoot(t *testing.T) (root, state string) {
	t.Helper()
	root = t.TempDir()
	state = filepath.Join(root, "state")
	conf := "state_dir: " + state + "\nlog_file: " + filepath.Join(root, "hub.log") + "\n"
	if err := os.WriteFile(filepath.Join(root, "hub.yaml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, state
}

// `evidence` refuses what it does not understand with 64, before reading
// or creating anything: the rule keys, jobs and extensions were brought
// to after they each created a directory on the way to saying no.
func TestEvidenceRefusesBeforeTouchingAnything(t *testing.T) {
	for _, tc := range []struct {
		args []string
		says string
	}{
		{[]string{"evidence"}, "evidence anchors <node>"},
		{[]string{"evidence", "lsit"}, `"lsit"`},
		{[]string{"evidence", "anchors"}, "needs a node"},
		{[]string{"evidence", "anchors", "a", "b"}, "takes one node"},
		{[]string{"evidence", "anchors", "../../etc/passwd"}, "node identity"},
	} {
		root, state := evidenceRoot(t)
		name := strings.Join(tc.args, " ")
		res := run(t, append(tc.args, "--root", root)...)
		if res.code != cli.ExitUsage {
			t.Errorf("%s: exited %d, want %d: %s", name, res.code, cli.ExitUsage, res.stderr)
		}
		if !strings.Contains(res.stderr, tc.says) {
			t.Errorf("%s: the refusal does not say %q: %s", name, tc.says, res.stderr)
		}
		for _, p := range []string{state, filepath.Join(root, "hub.log")} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("%s: created %s", name, p)
			}
		}
	}
}

// The file is printed as the hub stored it, byte for byte -- including a
// line this build would not parse, because the investigator is owed the
// record and not this build's reading of it.
func TestEvidenceAnchorsPrintsTheFileAsStored(t *testing.T) {
	root, state := evidenceRoot(t)
	dir := filepath.Join(state, "evidence-anchors")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stored := `{"seq":2,"hash":"sha256:00","received":"2026-10-06T12:00:00Z","result":"accepted","receipt":"x"}` + "\n" +
		"not json at all\n"
	if err := os.WriteFile(filepath.Join(dir, "web1.example.jsonl"), []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	res := run(t, "evidence", "anchors", "web1.example", "--root", root)
	if res.code != 0 {
		t.Fatalf("exited %d: %s", res.code, res.stderr)
	}
	if res.stdout != stored {
		t.Errorf("the output is not the file:\n got %q\nwant %q", res.stdout, stored)
	}

	res = run(t, "evidence", "anchors", "db1.example", "--root", root)
	if res.code != 1 || !strings.Contains(res.stderr, "no evidence anchors for db1.example") {
		t.Errorf("a node with no file: exited %d: %s", res.code, res.stderr)
	}
}
