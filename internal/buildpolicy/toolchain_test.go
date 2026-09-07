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

// The workflows install the toolchain go.mod pins, and nothing else.
//
// They used to say `go-version-file: go.mod`, which reads the `go`
// directive — a floor — rather than the `toolchain` directive, which is
// the pin. So a runner installed the floor from the action's own cached
// source and then fetched the pin from proxy.golang.org through
// GOTOOLCHAIN=auto: over the network, uncached, before a line of this
// repository compiled. Twelve jobs a push each carried that, and one of
// them died in setup-go's own `go env GOPATH` when the fetch failed.
//
// Naming the pin in the workflow removes the fetch and creates a
// coupling: two files now hold the same version. This is the guard for
// that coupling, and it is why naming it there is safe — a `toolchain`
// bump that forgets a workflow fails here rather than silently
// reintroducing the download, and a workflow that names a version go.mod
// does not pin fails here too.
func TestTheWorkflowsInstallTheToolchainGoModPins(t *testing.T) {
	root := repoRoot(t)
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`(?m)^toolchain go(1\.\d+(?:\.\d+)?)$`).FindStringSubmatch(string(mod))
	if pin == nil {
		t.Fatal("go.mod has no `toolchain` directive; TestGoModPinsTheToolchain says why that matters")
	}
	want := pin[1]

	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	version := regexp.MustCompile(`(?m)^\s*go-version:\s*"?([0-9][^"\s]*)"?\s*$`)
	fromFile := regexp.MustCompile(`(?m)^\s*go-version-file:`)

	steps := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		name := filepath.Join(".github", "workflows", e.Name())
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		body := string(b)
		if fromFile.MatchString(body) {
			t.Errorf("%s uses `go-version-file`, which reads go.mod's `go` floor rather "+
				"than its `toolchain` pin — so the runner installs one version and "+
				"downloads another from proxy.golang.org before anything compiles. "+
				"Name %s instead.", name, want)
		}
		for _, m := range version.FindAllStringSubmatch(body, -1) {
			steps++
			if m[1] != want {
				t.Errorf("%s installs Go %s and go.mod pins %s. A runner on a different "+
					"toolchain than the pin fetches the pin over the network, which is "+
					"the download this arrangement exists to remove.", name, m[1], want)
			}
		}
	}
	if steps == 0 {
		t.Fatal("no `go-version` was found in any workflow; this guard has stopped checking anything")
	}
	t.Logf("%d workflow steps install Go %s, which go.mod pins", steps, want)
}
