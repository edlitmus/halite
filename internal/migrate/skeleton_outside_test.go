package migrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A generated skeleton has to compile where the person porting a
// formula is working, which is not inside this repository.
//
// It did not. Every skeleton imported `internal/bridge` and called
// `bridge.Confine()`, so the tool whose entire purpose is to bootstrap a
// port produced a file that builds only here. Nobody chose that: the
// other tests in this file check the text the generator writes, and
// text that mentions the right identifiers is not the same as code that
// builds. This one puts it in a module of its own and runs the
// compiler.
func TestAGeneratedSkeletonCompilesOutsideThisRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a module")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no toolchain on this machine")
	}

	module := PyModule{
		File: "_modules/certificates.py",
		Name: "certificates",
		Functions: []PyFunction{
			{Name: "issue", Doc: "Issue one.", Params: []PyParam{
				{Name: "common_name"},
				{Name: "days", HasDefault: true, Default: "365"},
				{Name: "extra", Keywords: true},
			}},
			{Name: "revoke"},
		},
	}
	skeleton := GenerateSkeleton(module, "module")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(skeleton.Source), 0o644); err != nil {
		t.Fatal(err)
	}

	// A module that is not this one, requiring this one by a path, which
	// is what an author does before there is a tagged release.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	gomod := "module example.invalid/ported\n\ngo " + goDirective(t, root) + "\n\n" +
		"require github.com/edlitmus/halite v0.0.0\n\n" +
		"replace github.com/edlitmus/halite => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("a generated skeleton does not compile outside this repository:\n%s", out)
	}

	// And it reaches the public package rather than an internal one,
	// which is the property that made it fail.
	if strings.Contains(skeleton.Source, "/internal/") {
		t.Errorf("the skeleton imports an internal package:\n%s", skeleton.Source)
	}
}

// goDirective reads the `go` line out of this repository's go.mod, so
// the generated module asks for the same language version rather than a
// number this test would have to keep current.
func goDirective(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("this repository's go.mod names no go version")
	return ""
}
