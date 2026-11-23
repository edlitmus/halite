package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ci.yml's "what changed" job decides whether a change is documentation
// only, and so runs two legs instead of twenty, with one grep pattern. It
// read only docs/*.md, so a change to the handbook (docs/handbook/) or a
// proposal (docs/proposals/) ran every leg (DIVERGENCE 5.706). This reads
// the pattern out of the workflow and holds it to paths of both kinds, in
// both directions: a documentation path must be documentation, and a
// workflow, a Go file or a non-Markdown file under docs/ must stay code,
// because a skipped leg that should have run is the invisible failure.
func TestADocumentationChangeRunsOnlyTheDocLegs(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`grep -qvE '([^']+)'`).FindSubmatch(b)
	if m == nil {
		t.Fatal("ci.yml has no `grep -qvE '...'` deciding what is documentation; this test needs updating with it")
	}
	// grep -E and Go's regexp agree on this pattern's syntax: anchors,
	// bracket negation, alternation, `.*` and an escaped dot.
	doc := regexp.MustCompile(string(m[1]))

	for _, c := range []struct {
		path string
		doc  bool
	}{
		{"README.md", true},
		{"CHANGELOG.md", true},
		{"SPEC.md", true},
		{"docs/DIVERGENCE.md", true},
		{"docs/handbook/27-http-api.md", true},
		{"docs/proposals/765-signed-orchestration.md", true},
		{"docs/audits/2026-10-issue-321-second-pass.md", true},
		{"docs/handbook/CONTENTS", false},
		{".github/PULL_REQUEST_TEMPLATE.md", false},
		{".github/workflows/ci.yml", false},
		{"internal/hub/orch.go", false},
		{"contrib/tofu/README.md", false},
		{"cmd/halite-node/README.md", false},
	} {
		if got := doc.MatchString(c.path); got != c.doc {
			kind := map[bool]string{true: "documentation", false: "code"}
			t.Errorf("%s reads as %s; want %s", c.path, kind[got], kind[c.doc])
		}
	}
	if strings.Contains(string(m[1]), "docs/[^/]*") {
		t.Errorf("the pattern reads only the top of docs/ again: %s", m[1])
	}
}
