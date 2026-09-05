package metrics

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// registeredFamilies is every metric name this build names in its own
// source, which is the set an exposition can ever contain.
func registeredFamilies(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	name := regexp.MustCompile(`"(halite_[a-z0-9_]+)"`)
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "bin", "dist", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range name.FindAllStringSubmatch(string(body), -1) {
			out[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no metric families were found; this check has stopped checking")
	}
	return out
}

// A metric named in the documentation has to be one this build can
// actually expose.
//
// The operations guide listed `halite_reactor_queue_depth` among the
// things to alert on without saying it appears only once a reactor is
// running, and named nothing at all about the relay families or the
// eleven SPEC 26.2 names that are not registered. An alert written
// against a metric that never appears does not fail — it stays silent,
// which is what it would do if everything were fine.
func TestEveryDocumentedMetricExists(t *testing.T) {
	registered := registeredFamilies(t)

	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "metrics.md"))
	if err != nil {
		t.Fatal(err)
	}
	rest := string(body)

	// Not the section whose whole purpose is naming what is absent.
	const absent = "### What SPEC 26.2 names and this build does not have"
	cut := strings.Index(rest, absent)
	if cut < 0 {
		t.Fatal("metrics.md no longer names what is missing; this check is reading the wrong file")
	}
	end := strings.Index(rest[cut:], "## The series cap")
	if end < 0 {
		t.Fatal("the absent-metrics section has no end; this check cannot skip it")
	}
	rest = rest[:cut] + rest[cut+end:]
	// And not the subsection whose whole purpose is naming what is
	// absent.
	if cut := strings.Index(rest, "#### What SPEC 26.2 names and this build does not have"); cut >= 0 {
		if end := strings.Index(rest[cut:], "### The series cap"); end > 0 {
			rest = rest[:cut] + rest[cut+end:]
		}
	}

	// Backticked, which is how a family is written and prose is not.
	named := regexp.MustCompile("`(halite_[a-z0-9_]+)`")
	checked := 0
	for i, line := range strings.Split(rest, "\n") {
		for _, m := range named.FindAllStringSubmatch(line, -1) {
			checked++
			if !registered[m[1]] {
				t.Errorf("docs/metrics.md:%d names %s, which this build never registers",
					i+1, m[1])
			}
		}
	}
	if checked == 0 {
		t.Error("no documented metric names were checked")
	}
	t.Logf("checked %d documented metric references", checked)
}

// And the other direction: a family this build registers that the
// documentation never mentions.
//
// TestEveryDocumentedMetricExists catches a document naming a metric
// that is not there, which is the failure that produces a silent alert.
// This catches the opposite one, which is quieter still: a family is
// registered, exposed on every scrape, and named in no document, so the
// only way to find it is to read the source or to notice an unfamiliar
// series in Grafana. Nine families arrived in one change and the count
// of what was documented was checked by hand; a hand is what this
// replaces.
//
// The exposition itself is the list of what has to be explained. There
// is no allowlist here on purpose: a family worth exposing to an
// operator is worth a line saying what a reading of it means.
func TestEveryRegisteredMetricIsDocumented(t *testing.T) {
	registered := registeredFamilies(t)

	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "metrics.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Backticked, which is how a family is written, and how the check in
	// the other direction reads them too.
	named := regexp.MustCompile("`(halite_[a-z0-9_]+)`")
	documented := map[string]bool{}
	for _, m := range named.FindAllStringSubmatch(string(body), -1) {
		documented[m[1]] = true
	}

	missing := 0
	for name := range registered {
		if documented[name] {
			continue
		}
		missing++
		t.Errorf("%s is registered and docs/metrics.md never names it; "+
			"an operator meets it first as an unfamiliar series on a graph", name)
	}
	t.Logf("checked %d registered families, %d undocumented", len(registered), missing)
}
