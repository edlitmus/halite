package buildpolicy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/repotree"
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

// TestLexiconPolicy enforces SPEC section 2.3: the prohibited terms may
// not appear in source, configuration keys, log output, metric names,
// documentation, or test fixtures, outside the compatibility shim.
func TestLexiconPolicy(t *testing.T) {
	findings, err := Scan(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("the lexicon policy of SPEC section 2.3 is violated:\n")
	for _, f := range findings {
		b.WriteString("  " + f.String() + "\n")
	}
	b.WriteString("\nIf quoting Salt is genuinely unavoidable on a line, mark it with the lexicon allow comment.")
	t.Error(b.String())
}

// TestLexiconScannerActuallyMatches guards the guard: a scanner that
// silently matches nothing would pass the policy test forever.
func TestLexiconScannerActuallyMatches(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.go")
	body := "package x\n\n// The " + "master" + " sends to each " + "minion" + ".\nvar X = \"" + "whitelist" + "\"\n"
	if err := os.WriteFile(bad, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 3 {
		t.Fatalf("scanner found %d findings, want 3: %v", len(findings), findings)
	}
	for _, f := range findings {
		if f.Line == 0 || f.File == "" || f.Term.Required == "" {
			t.Errorf("a finding must name the file, the line, and the replacement: %+v", f)
		}
	}
}

func TestLexiconAllowMarkerSuppressesOneLine(t *testing.T) {
	dir := t.TempDir()
	body := "package x\n\nvar X = \"" + "minion" + "\" // " + lexiconAllowMarker + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ok.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("the allow marker should suppress the line, got %v", findings)
	}
}

// AllowedModules is the dependency allowlist of SPEC section 4.2. Adding
// one requires an ADR, a named reviewer, and an amendment to that section.
var AllowedModules = map[string]string{
	"golang.org/x/sys":  "tier 1: Windows, BSD, and Solaris syscalls, maintained by the Go team, pure Go",
	"golang.org/x/term": "tier 2: terminal size and raw mode for CLI prompts; removable",
}

// TestDependencyAllowlist fails if any module outside SPEC section 4.2
// appears in the build graph at any depth.
//
// # It read `go list -m all`, and therefore never ran
//
// That command cannot succeed in a vendored repository:
//
//	go: can't compute 'all' using the vendor directory
//
// The old body treated the failure as environmental and skipped. It is not
// environmental, it is a property of this repository -- so the test skipped
// on every machine, including CI, and `make policy` runs without `-v`, so
// the skip printed nothing. `policy` is also the one leg that runs on a
// docs-only change. CLAUDE.md names this test as what enforces the
// allowlist, and it had never enforced anything, anywhere.
//
// # It reads the files instead
//
// `vendor/modules.txt` is the build graph of a vendored build: it lists
// every module that provides a package this tree imports, at any depth,
// which is exactly the question. `go.mod`'s requirements are read too, so
// that a module required and not yet vendored is caught as well.
//
// Both are files in the repository, so this answers offline, on every
// platform, with no toolchain state. And a source that cannot be read is a
// failure rather than a skip: the unreadable case is the one where the
// check has stopped checking.
func TestDependencyAllowlist(t *testing.T) {
	root := repoRoot(t)
	self := "github.com/edlitmus/halite"

	inGraph := map[string]string{}
	for mod, where := range modulesFromVendor(t, root) {
		inGraph[mod] = where
	}
	for mod, where := range modulesFromGoMod(t, root) {
		if _, seen := inGraph[mod]; !seen {
			inGraph[mod] = where
		}
	}
	if len(inGraph) == 0 {
		t.Fatal("no dependency was read out of vendor/modules.txt or go.mod; " +
			"this check has stopped checking")
	}

	var offenders []string
	for mod, where := range inGraph {
		if mod == self || strings.HasPrefix(mod, self+"/") {
			continue
		}
		if _, ok := AllowedModules[mod]; ok {
			continue
		}
		offenders = append(offenders, mod+" ("+where+")")
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("modules outside the SPEC section 4.2 allowlist are in the build graph: %s\n"+
			"Adding one requires an ADR, a named reviewer, and an amendment to that section.",
			strings.Join(offenders, ", "))
	}
	// Named, because "the allowlist passed" and "the allowlist read
	// nothing" printed the same thing for as long as this skipped.
	checked := make([]string, 0, len(inGraph))
	for mod := range inGraph {
		checked = append(checked, mod)
	}
	sort.Strings(checked)
	t.Logf("checked %d module(s) against SPEC 4.2's allowlist: %s",
		len(checked), strings.Join(checked, ", "))
}

// modulesFromVendor reads the build graph out of vendor/modules.txt, whose
// module lines are `# <path> <version>`.
func modulesFromVendor(t *testing.T, root string) map[string]string {
	t.Helper()
	path := filepath.Join(root, "vendor", "modules.txt")
	body, err := os.ReadFile(path)
	if err != nil {
		// A vendored tree is what this project ships; a missing file here
		// is a repository that has changed shape, and silence about it is
		// how this check came to run nowhere.
		t.Fatalf("vendor/modules.txt could not be read, so the dependency allowlist "+
			"cannot be checked: %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "# "))
		if len(fields) > 0 {
			out[fields[0]] = "vendor/modules.txt"
		}
	}
	return out
}

// modulesFromGoMod reads the requirements, so that a module required and
// not yet vendored is caught too.
func modulesFromGoMod(t *testing.T, root string) map[string]string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("go.mod could not be read: %v", err)
	}
	out := map[string]string{}
	inBlock := false
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "require (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		}
		fields := strings.Fields(line)
		if inBlock {
			if len(fields) >= 1 && fields[0] != "" {
				out[fields[0]] = "go.mod"
			}
			continue
		}
		// The single-line form: `require golang.org/x/sys v0.47.0`.
		if len(fields) >= 2 && fields[0] == "require" {
			out[fields[1]] = "go.mod"
		}
	}
	return out
}

// TestBuildRecipePinsIntegrityFlags holds the build recipe to SPEC
// section 4.3. The assertion is on the recipe rather than on this test
// binary, because a `go test` run is not a shipped artifact: what matters
// is that every artifact the Makefile produces is built with cgo off, a
// trimmed path, and vendored modules.
func TestBuildRecipePinsIntegrityFlags(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("the build recipe is missing: %v", err)
	}

	// The comments are dropped first, and that is the whole of this
	// test's history.
	//
	// It was `strings.Contains` over the entire file, and the file opens
	// with a comment block listing all five flags -- the block this
	// check was written from. So stripping `-trimpath`, `CGO_ENABLED=0`
	// and `GOPROXY=off` from the actual recipe left it green: the gate
	// was reading the documentation of itself. DIVERGENCE 5.140.
	//
	// It also asks *where* now, not merely whether. A flag present in
	// some unrelated recipe is not a flag on the artifact, so the
	// assertions are against the variables the release build uses and
	// against the release target using them.
	recipe := stripMakeComments(string(b))

	required := map[string]struct{ variable, why string }{
		"CGO_ENABLED=0":       {"RELEASE_ENV", "no cgo, so the artifact links no shared object"},
		"GOFLAGS=-mod=vendor": {"RELEASE_ENV", "builds read the vendored allowlist"},
		"GOPROXY=off":         {"RELEASE_ENV", "the build network is disabled"},
		"-trimpath":           {"BUILDFLAGS", "reproducible paths"},
		"-buildvcs=true":      {"BUILDFLAGS", "the artifact records the source commit"},
	}
	for flag, want := range required {
		line := makeAssignment(recipe, want.variable)
		if line == "" {
			t.Errorf("the Makefile has no %s assignment, so SPEC 4.3's %s cannot be checked",
				want.variable, flag)
			continue
		}
		if !strings.Contains(line, flag) {
			t.Errorf("%s does not set %s (%s); SPEC section 4.3 requires it.\n\t%s = %s",
				want.variable, flag, want.why, want.variable, line)
		}
	}

	// And the release target has to use them, or the flags are set and
	// not applied -- which reads exactly like being set.
	target := makeTargetBody(recipe, "release")
	if target == "" {
		t.Fatal("the Makefile has no `release` target, so nothing here is checking an artifact")
	}
	for _, use := range []string{"$(RELEASE_ENV)", "$(BUILDFLAGS)"} {
		if !strings.Contains(target, use) {
			t.Errorf("the release target does not use %s, so SPEC 4.3's flags do not reach "+
				"the artifact:\n%s", use, target)
		}
	}
}

// stripMakeComments removes whole-line comments, which is what let the
// check above read its own documentation instead of the recipe.
func stripMakeComments(recipe string) string {
	var kept []string
	for _, line := range strings.Split(recipe, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// makeAssignment returns the right-hand side of `NAME = ...` or `NAME != ...`.
func makeAssignment(recipe, name string) string {
	for _, line := range strings.Split(recipe, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, name) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, name))
		for _, op := range []string{"!=", "=", ":="} {
			if strings.HasPrefix(rest, op) {
				return strings.TrimSpace(strings.TrimPrefix(rest, op))
			}
		}
	}
	return ""
}

// makeTargetBody returns the recipe lines of a target, which are the
// indented lines following `name:`.
func makeTargetBody(recipe, name string) string {
	lines := strings.Split(recipe, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, name+":") {
			continue
		}
		var body []string
		for _, next := range lines[i+1:] {
			if next != "" && !strings.HasPrefix(next, "\t") && !strings.HasPrefix(next, " ") {
				break
			}
			body = append(body, next)
		}
		return strings.Join(body, "\n")
	}
	return ""
}

// TestNoMathRand enforces the import check of SPEC section 25.3:
// math/rand appears nowhere outside the deterministic template seed.
func TestNoMathRand(t *testing.T) {
	root := repoRoot(t)
	allowed := map[string]bool{
		// SPEC section 10.2.4 requires the template random source to be
		// deterministic per node and job, which crypto/rand cannot be.
		"internal/template/env.go":  true,
		"internal/template/eval.go": true,
	}
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if rel == "vendor" || rel == ".git" || repotree.OtherCheckout(root, path) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || allowed[rel] || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), `"math/rand"`) || strings.Contains(string(b), `"math/rand/v2"`) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("math/rand is imported outside the deterministic template seed: %s\n"+
			"SPEC section 25.3 requires crypto/rand everywhere else.", strings.Join(offenders, ", "))
	}
}
