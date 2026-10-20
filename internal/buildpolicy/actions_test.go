package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every action a workflow runs is named by commit, not by tag.
//
// A tag is a pointer its owner can move, and every workflow here named
// its actions that way: checkout@v7, setup-go@v7, the artifact actions,
// attest-build-provenance@v4 -- which runs in the one job holding
// `id-token: write` and `attestations: write`, so whoever can move that
// tag can sign provenance for this repository -- and a third-party
// action, cross-platform-actions/action@v1.6.0, that boots the FreeBSD
// VM. DIVERGENCE 5.421.
//
// The tag is kept in a trailing comment so a reader, and a bump, can see
// what the commit was meant to be. This test cannot check that the two
// agree -- that needs the network -- so it holds the shape: a full
// 40-hex commit and a comment naming a version. A `uses:` with neither is
// how the next unpinned action would arrive.
func TestEveryActionIsPinnedByCommit(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	uses := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*(.*?)\s*$`)
	pinned := regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40} # v\d+(\.\d+)*$`)
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		rel := filepath.Join(".github", "workflows", e.Name())
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range uses.FindAllStringSubmatch(string(b), -1) {
			n++
			if !pinned.MatchString(m[1]) {
				t.Errorf("%s: `uses: %s` is not pinned to a commit. Name the action as "+
					"owner/repo@<40-hex commit> # <tag>; a tag is a pointer its owner can move.",
					rel, m[1])
			}
		}
	}
	if n == 0 {
		t.Fatal("no `uses:` was found in any workflow; this guard has stopped checking anything")
	}
	t.Logf("%d action references, each pinned to a commit", n)
}

// A job that hands its working tree to cross-platform-actions does not
// leave the checkout's token in it.
//
// actions/checkout v7 keeps the job's token unless told not to, in
// _temp/git-credentials-*.config with an includeIf in .git/config, and
// the VM action rsyncs /home/runner/work/ -- that file included, by the
// exclusions its own log prints -- into a machine it runs. Nothing in
// those jobs talks to GitHub from the VM.
func TestTheVMJobsCheckOutWithoutTheToken(t *testing.T) {
	root := repoRoot(t)
	n := 0
	for _, name := range []string{"ci.yml", "fleet.yml"} {
		rel := filepath.Join(".github", "workflows", name)
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for job, body := range splitJobs(t, string(b)) {
			if !strings.Contains(body, "cross-platform-actions/action@") {
				continue
			}
			n++
			steps := splitSteps(body)
			for _, s := range steps {
				if !strings.Contains(s, "actions/checkout@") {
					continue
				}
				if !regexp.MustCompile(`(?m)^\s*persist-credentials:\s*false\s*$`).MatchString(s) {
					t.Errorf("%s job %s checks out with the token kept, then "+
						"copies the work directory into a VM; set persist-credentials: false", rel, job)
				}
			}
		}
	}
	if n < 2 {
		t.Fatalf("found %d jobs using cross-platform-actions, want ci.yml's and fleet.yml's; "+
			"this guard has stopped checking anything", n)
	}
}
