package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A push to main is never cancelled by the next one.
//
// ci.yml grouped every run by ref with `cancel-in-progress: true`, which
// is right for a pull request and wrong for main: there, each commit is
// a merge, and a cancelled run leaves it with no CI result at all. At
// least ten main commits between 2026-10-09 and 2026-10-18 ended that
// way. DIVERGENCE 5.423.
//
// Held on any workflow that declares a concurrency group and is
// triggered by a push: the group must separate pushes by commit, and
// cancelling must be confined to pull requests.
func TestAPushToMainIsNotCancelledByTheNext(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?m)^concurrency:\n((?:[ \t]+.*\n)+)`)
	group := regexp.MustCompile(`(?m)^\s*group:\s*(.*)$`)
	cancel := regexp.MustCompile(`(?m)^\s*cancel-in-progress:\s*(.*)$`)
	push := regexp.MustCompile(`(?m)^  push:`)
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		rel := filepath.Join(".github", "workflows", e.Name())
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		m := block.FindStringSubmatch(body)
		if m == nil || !push.MatchString(body) {
			continue
		}
		checked++
		g := group.FindStringSubmatch(m[1])
		c := cancel.FindStringSubmatch(m[1])
		if c == nil || strings.TrimSpace(c[1]) == "false" {
			continue
		}
		if strings.TrimSpace(c[1]) != "${{ github.event_name == 'pull_request' }}" {
			t.Errorf("%s cancels in progress on `%s`, which reaches pushes to main; "+
				"confine it to pull requests", rel, strings.TrimSpace(c[1]))
		}
		if g == nil || !strings.Contains(g[1], "github.sha") {
			t.Errorf("%s groups runs without the commit, so a push to main can be cancelled "+
				"as pending by the push after the next; group pushes by github.sha", rel)
		}
	}
	if checked == 0 {
		t.Fatal("no workflow triggered by a push declares a concurrency group; this guard " +
			"has stopped checking anything")
	}
}
