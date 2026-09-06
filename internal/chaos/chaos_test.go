package chaos

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the module root")
		}
		dir = parent
	}
}

// Every scenario SPEC 31 names is registered, and nothing is registered
// that SPEC does not name — except the one that says why.
//
// The Chaos row is a comma-separated list in a table cell, and matching
// it is the point: a scenario added to the specification and not here
// would otherwise be a row nobody built, which is what the whole layer
// was until this package existed.
func TestTheRegistryMatchesTheSpec(t *testing.T) {
	inSpec := specChaosScenarios(t)
	if len(inSpec) == 0 {
		t.Fatal("no scenarios were read out of SPEC 31's Chaos row")
	}

	// Case-insensitively: SPEC capitalises the first scenario in the
	// cell because it starts the sentence, and a registry that had to
	// copy that would be encoding where a comma happens to fall.
	registered := map[string]Scenario{}
	for _, s := range Scenarios {
		if s.Spec == "" {
			continue // deliberately not one of SPEC's; see below
		}
		key := strings.ToLower(s.Spec)
		if prev, dup := registered[key]; dup {
			t.Errorf("%q is registered twice, as %s and %s", s.Spec, prev.Key, s.Key)
		}
		registered[key] = s
	}

	for _, name := range inSpec {
		if _, ok := registered[strings.ToLower(name)]; !ok {
			t.Errorf("SPEC 31's Chaos row names %q and this registry does not have it. "+
				"A scenario SPEC names and nothing defines is the state this whole "+
				"package was created out of.", name)
		}
	}
	seen := map[string]bool{}
	for _, name := range inSpec {
		seen[strings.ToLower(name)] = true
	}
	for spec, s := range registered {
		if !seen[spec] {
			t.Errorf("%s is registered as SPEC 31's %q and the Chaos row does not say that; "+
				"either the wording changed or this is not one of SPEC's scenarios, in "+
				"which case leave Spec empty and say why in a comment", s.Key, spec)
		}
	}
	t.Logf("SPEC 31 names %d chaos scenarios; %d registered against it, %d beyond it",
		len(inSpec), len(registered), len(Scenarios)-len(registered))
}

// Every scenario has a test that says it exercises it.
//
// This is the half that makes the registry more than a list of
// intentions. `Exercises` is the marker, and it is found by reading the
// tree rather than by running anything, because the tests live in the
// packages whose machinery they drive — the hub's lab is unexported and
// there is no way to reach it from here.
//
// The mapping from constant to key is read out of this package's own
// source, so renaming a constant cannot quietly orphan a scenario.
func TestEveryScenarioHasATestThatExercisesIt(t *testing.T) {
	root := repoRoot(t)
	byIdent := scenarioConstants(t, root)
	if len(byIdent) != len(Scenarios) {
		t.Fatalf("this package declares %d scenario constants and %d scenarios; "+
			"the two are meant to be the same list", len(byIdent), len(Scenarios))
	}

	exercised := map[string][]string{}
	// Qualified only. A scenario test drives another package's
	// machinery -- the hub's lab is unexported and there is nothing
	// here to exercise -- so an unqualified `Exercises(` in this
	// package is this package's own tests calling it, not a scenario
	// being covered.
	call := regexp.MustCompile(`chaos\.Exercises\(chaos\.([A-Za-z0-9_]+)\)`)

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
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		body := string(b)
		rel, _ := filepath.Rel(root, path)
		for _, m := range call.FindAllStringSubmatch(body, -1) {
			record(t, exercised, byIdent, m[1], rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range Scenarios {
		files := exercised[s.Key]
		if len(files) == 0 {
			t.Errorf("%s has a defined behaviour and no test that exercises it. "+
				"SPEC 31 asks for \"a defined, tested, documented behaviour\", and "+
				"this one has two of the three.\n  behaviour: %s", s.Key, s.Behaviour)
			continue
		}
		t.Logf("%s: %s", s.Key, strings.Join(files, ", "))
	}
}

func record(t *testing.T, into map[string][]string, byIdent map[string]string, ident, file string) {
	t.Helper()
	key, ok := byIdent[ident]
	if !ok {
		t.Errorf("%s: chaos.Exercises names %s, which is not a scenario constant", file, ident)
		return
	}
	for _, seen := range into[key] {
		if seen == file {
			return
		}
	}
	into[key] = append(into[key], file)
}

// Every scenario says what its test does not establish.
//
// A chaos suite that reports only what it covers is the same reassurance
// the absence of one gave: every scenario here is a lab, and a lab is
// not an estate. The Limit field is where that is written down, and it
// being required is what stops it being skipped for the easy ones.
func TestEveryScenarioSaysWhatItDoesNotEstablish(t *testing.T) {
	for _, s := range Scenarios {
		if strings.TrimSpace(s.Behaviour) == "" {
			t.Errorf("%s has no defined behaviour", s.Key)
		}
		if strings.TrimSpace(s.Limit) == "" {
			t.Errorf("%s does not say what its test leaves unestablished. Every one of "+
				"these has something; the ones that look like they do not are the "+
				"ones worth writing down.", s.Key)
		}
		if s.Key == "" {
			t.Error("a scenario has no key")
		}
	}
}

// specChaosScenarios reads the Chaos row of SPEC 31's table.
func specChaosScenarios(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	spec := string(b)
	start := strings.Index(spec, "## 31. Testing and validation")
	if start < 0 {
		t.Fatal("SPEC.md has no section 31; this audit is reading a document it was not written for")
	}
	section := spec[start:]
	if end := strings.Index(section, "\n## "); end > 0 {
		section = section[:end]
	}
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| Chaos ") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 2 {
			t.Fatalf("SPEC 31's Chaos row has %d cells; this audit expects two", len(cells))
		}
		// "a, b, c. Each has a defined..." — the list ends at the full
		// stop, and the sentence after it is the requirement rather
		// than a scenario.
		list := strings.TrimSpace(cells[1])
		if i := strings.Index(list, ". "); i > 0 {
			list = list[:i]
		}
		var out []string
		for _, name := range strings.Split(list, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
		return out
	}
	t.Fatal("SPEC 31 has no Chaos row; this audit is reading the wrong table")
	return nil
}

// scenarioConstants reads this package's own constant block, so the
// mapping from identifier to key is the source rather than a copy.
func scenarioConstants(t *testing.T, root string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "internal", "chaos", "chaos.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	start := strings.Index(body, "const (")
	if start < 0 {
		t.Fatal("internal/chaos/chaos.go has no constant block")
	}
	block := body[start:]
	if end := strings.Index(block, "\n)"); end > 0 {
		block = block[:end]
	}
	decl := regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_]+)\s*=\s*"([^"]+)"`)
	out := map[string]string{}
	for _, m := range decl.FindAllStringSubmatch(block, -1) {
		if _, ok := Lookup(m[2]); !ok {
			t.Errorf("the constant %s names %q and no scenario has that key", m[1], m[2])
			continue
		}
		out[m[1]] = m[2]
	}
	return out
}

// An unknown key panics rather than passing as a covered scenario.
func TestExercisingAnUnknownScenarioPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("chaos.Exercises accepted a key that is not a scenario; a test claiming " +
				"to cover something unregistered would leave the scenario reported as " +
				"uncovered while its test appears to pass")
		}
	}()
	_ = Exercises("no-such-scenario")
}

// And a known key returns the scenario, which is what a test logs.
func TestExercisingAKnownScenarioReturnsIt(t *testing.T) {
	for _, key := range Keys() {
		s := Exercises(key)
		if s.Key != key {
			t.Errorf("Exercises(%q) returned %q", key, s.Key)
		}
	}
	if _, ok := Lookup(fmt.Sprintf("%s-not", HubRestartMidJob)); ok {
		t.Error("Lookup matched a key it should not have")
	}
}
