package specaudit

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestLedgerTestCitationsResolve holds the ledger to naming tests that
// exist.
//
// It cited `TestSingleLetterBooleansStayStrings` as the evidence for
// divergence 1.1; the test is called `TestSingleLetterYNStayStrings`.
// A citation is the whole of the argument that a divergence is
// deliberate rather than a bug, so one pointing at nothing is worse than
// no citation at all — a reader who goes looking finds an empty result
// and cannot tell whether the test was renamed or never written.
func TestLedgerTestCitationsResolve(t *testing.T) {
	ledger := repoFile(t, ledgerFile)

	defined := map[string]bool{}
	root := filepath.Join("..", "..")
	funcName := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "bin", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range funcName.FindAllStringSubmatch(string(body), -1) {
			defined[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defined) == 0 {
		t.Fatal("no test functions were found; this check has stopped checking")
	}

	cited := regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")
	checked := 0
	for i, line := range strings.Split(ledger, "\n") {
		for _, m := range cited.FindAllStringSubmatch(line, -1) {
			checked++
			if !defined[m[1]] {
				t.Errorf("%s:%d cites %s, which no test defines", ledgerFile, i+1, m[1])
			}
		}
	}
	if checked == 0 {
		t.Error("no test citations were checked")
	}
	t.Logf("checked %d test citations", checked)
}

// TestLedgerMetricGapMatchesTheBuild holds section 5.23's arithmetic to
// the source.
//
// It says SPEC 26.2 names thirty-two families and this build registers
// twenty-one, and lists the eleven that are missing. Every number there
// is a count someone did once, and each is wrong the moment a family is
// registered — at which point the ledger claims a gap that is filled,
// which is the failure this package exists to prevent.
func TestLedgerMetricGapMatchesTheBuild(t *testing.T) {
	spec := repoFile(t, specFile)
	start := strings.Index(spec, "### 26.2 Metrics")
	if start < 0 {
		t.Fatal("SPEC.md has no 26.2; this check is reading the wrong file")
	}
	end := strings.Index(spec[start:], "26.3")
	if end < 0 {
		t.Fatal("SPEC 26.2 has no end")
	}

	// A family is written with its label set; the name is what matters.
	named := regexp.MustCompile("`(halite_[a-z0-9_]+)(?:\\{[^}]*\\})?`")
	inSpec := map[string]bool{}
	for _, m := range named.FindAllStringSubmatch(spec[start:start+end], -1) {
		inSpec[m[1]] = true
	}
	if len(inSpec) == 0 {
		t.Fatal("no families were read out of SPEC 26.2")
	}

	registered := registeredMetricFamilies(t)
	var missing []string
	for name := range inSpec {
		if !registered[name] {
			missing = append(missing, name)
		}
	}

	ledger := repoFile(t, ledgerFile)
	for _, name := range missing {
		if !strings.Contains(ledger, "`"+name+"`") {
			t.Errorf("%s does not record %s, which SPEC 26.2 names and this "+
				"build does not register", ledgerFile, name)
		}
	}
	// And the other direction: a family the ledger calls missing that
	// has since been registered.
	// Bounded at the next heading, not at section 6: the entries after
	// it name registered families in prose while explaining what goes
	// missing without them.
	section := ledger[strings.Index(ledger, "### 5.23"):]
	if cut := strings.Index(section[len("### 5.23"):], "\n### "); cut > 0 {
		section = section[:len("### 5.23")+cut]
	}
	for _, m := range regexp.MustCompile("`(halite_[a-z0-9_]+)`").
		FindAllStringSubmatch(section, -1) {
		if registered[m[1]] {
			t.Errorf("5.23 records %s as not registered, and it is", m[1])
		}
	}

	t.Logf("SPEC 26.2 names %d families, %d registered, %d missing",
		len(inSpec), len(inSpec)-len(missing), len(missing))
}

// registeredMetricFamilies is every metric name the build names outside
// its tests, which is the set an exposition can contain.
func registeredMetricFamilies(t *testing.T) map[string]bool {
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
