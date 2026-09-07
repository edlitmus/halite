package specaudit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// SPEC 31's Upgrade row, held to the tests that cover it.
//
// The same shape as internal/chaos's guard and deliberately not a second
// copy of its machinery: chaos has eight scenarios with a defined
// behaviour each and needed a registry, and this row names three clauses
// and needs a table. What both need is for a row of SPEC 31 to be unable
// to sit there with nothing behind it, which is what this row did until
// 2026-09-06.
//
// The clause each test covers is named in the test's own file by a
// comment marker, so a test that stops covering a clause has to say so
// rather than drift quietly.
var upgradeClauses = []struct {
	// Clause is SPEC 31's own wording, matched against the row.
	Clause string
	// Marker is what a covering test writes in a comment.
	Marker string
}{
	{"Hub at version N with nodes at N-1 and N+1", "upgrade:version-skew"},
	{"state and job cache format migration", "upgrade:cache-format"},
	{"certificate rotation across an upgrade", "upgrade:cert-rotation"},
}

// Every clause of SPEC 31's Upgrade row is named by SPEC and covered by
// a test, and nothing is claimed that SPEC does not name.
func TestEveryUpgradeClauseHasATest(t *testing.T) {
	row := upgradeRow(t)
	for _, c := range upgradeClauses {
		if !strings.Contains(strings.ToLower(row), strings.ToLower(c.Clause)) {
			t.Errorf("SPEC 31's Upgrade row no longer says %q; the row is now:\n  %s",
				c.Clause, row)
		}
	}
	// And nothing in the row is unaccounted for. Split on the semicolons
	// SPEC uses to separate the clauses, so a fourth one added to the
	// row fails this rather than being quietly uncovered.
	clauses := strings.Split(strings.TrimSuffix(strings.TrimSpace(row), "."), ";")
	if len(clauses) != len(upgradeClauses) {
		t.Errorf("SPEC 31's Upgrade row has %d clauses and this audit knows %d. A clause "+
			"added to the specification and not here is a row nobody built.\n  %s",
			len(clauses), len(upgradeClauses), row)
	}

	covered := markersInTree(t)
	for _, c := range upgradeClauses {
		files := covered[c.Marker]
		if len(files) == 0 {
			t.Errorf("no test covers %q. SPEC 31 names it; a test claims it by writing "+
				"%s in a comment.", c.Clause, c.Marker)
			continue
		}
		t.Logf("%s: %s", c.Clause, strings.Join(files, ", "))
	}
}

// upgradeRow reads the second cell of SPEC 31's Upgrade row.
func upgradeRow(t *testing.T) string {
	t.Helper()
	spec := repoFile(t, specFile)
	for _, line := range strings.Split(spec, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| Upgrade ") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 2 {
			t.Fatalf("SPEC 31's Upgrade row has %d cells; this audit expects two", len(cells))
		}
		return strings.TrimSpace(cells[1])
	}
	t.Fatal("SPEC 31 has no Upgrade row; this audit is reading the wrong table")
	return ""
}

// markersInTree finds every `upgrade:` marker in a test file.
func markersInTree(t *testing.T) map[string][]string {
	t.Helper()
	root := filepath.Join("..", "..")
	marker := regexp.MustCompile(`upgrade:[a-z-]+`)
	out := map[string][]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == "vendor" || name == ".git" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Not this file: it holds every marker as data, and counting it
		// as coverage would make the guard pass on its own definitions.
		if strings.HasSuffix(path, "upgrade_test.go") && strings.Contains(path, "specaudit") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range marker.FindAllString(string(b), -1) {
			for _, seen := range out[m] {
				if seen == rel {
					goto next
				}
			}
			out[m] = append(out[m], rel)
		next:
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
