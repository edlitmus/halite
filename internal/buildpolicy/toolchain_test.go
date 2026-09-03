package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// SPEC 4.3 names the `toolchain` directive as the pin that makes a build
// reproducible, and nothing checked that it was there.
//
// It was removed by an edit that raised the `go` directive to work
// around a failure on one developer's machine. The build still worked,
// the tests still passed, and the reproducibility control the
// specification names by name was simply gone — which is the shape of
// every control that has no test behind it.
func TestGoModPinsTheToolchain(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	// The `go` directive is a floor, not a pin: a newer toolchain
	// satisfies it silently, which is how raising it removed the pin
	// without anything noticing. Naming the same version in both is not
	// an escape — Go rejects that as redundant before a test can run.
	pin := regexp.MustCompile(`(?m)^toolchain go1\.\d+(\.\d+)?$`)
	if !pin.MatchString(text) {
		t.Errorf("go.mod has no `toolchain` directive. SPEC section 4.3 requires "+
			"the Go toolchain be pinned there, because two builders cannot agree "+
			"on a digest while each picks its own compiler. go.mod begins:\n%s",
			firstLines(text, 6))
	}

}

// TestGoModFloorMatchesTheSpec holds the `go` directive to the version
// SPEC 4.1 names.
//
// SPEC said "Go 1.25 or later" while go.mod required 1.26. Both were
// true and they did not agree, which is the drift this package exists to
// catch — a reader planning from the specification would have built with
// a toolchain the module refuses.
func TestGoModFloorMatchesTheSpec(t *testing.T) {
	root := repoRoot(t)
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile(filepath.Join(root, "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}

	inMod := regexp.MustCompile(`(?m)^go (1\.\d+)`).FindStringSubmatch(string(mod))
	if inMod == nil {
		t.Fatal("go.mod has no `go` directive")
	}
	inSpec := regexp.MustCompile(`\*\*Go (1\.\d+) or later\.\*\*`).FindStringSubmatch(string(spec))
	if inSpec == nil {
		t.Fatal("SPEC 4.1 no longer states a Go version; this check is reading the wrong file")
	}
	if inMod[1] != inSpec[1] {
		t.Errorf("go.mod requires Go %s and SPEC 4.1 says %s or later. Both can be "+
			"true and they still disagree: somebody planning from the "+
			"specification builds with a toolchain the module refuses.",
			inMod[1], inSpec[1])
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
