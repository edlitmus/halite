package buildpolicy

import (
	"os"
	"os/exec"
	"path/filepath"
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
func TestDependencyAllowlist(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Path}}", "all")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list is unavailable in this environment: %v", err)
	}

	self := "github.com/edlitmus/halite"
	var offenders []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		mod := strings.TrimSpace(line)
		if mod == "" || mod == self || strings.HasPrefix(mod, self+"/") {
			continue
		}
		if _, ok := AllowedModules[mod]; ok {
			continue
		}
		offenders = append(offenders, mod)
	}
	if len(offenders) > 0 {
		t.Errorf("modules outside the SPEC section 4.2 allowlist are in the build graph: %s\n"+
			"Adding one requires an ADR, a named reviewer, and an amendment to that section.",
			strings.Join(offenders, ", "))
	}
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
	recipe := string(b)
	required := map[string]string{
		"CGO_ENABLED=0":       "no cgo, so the artifact links no shared object",
		"-trimpath":           "reproducible paths",
		"-buildvcs=true":      "the artifact records the source commit",
		"GOFLAGS=-mod=vendor": "builds read the vendored allowlist",
		"GOPROXY=off":         "the build network is disabled",
	}
	for flag, why := range required {
		if !strings.Contains(recipe, flag) {
			t.Errorf("the Makefile does not set %s (%s); SPEC section 4.3 requires it", flag, why)
		}
	}
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
			if rel == "vendor" || rel == ".git" {
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
