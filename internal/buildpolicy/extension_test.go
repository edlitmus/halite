package buildpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every extension this tree ships applies the limits the host asked for.
//
// The resource limits of SPEC 24.3 are set by the child on itself:
// `setrlimit` bounds the calling process, so a host cannot set a child's
// without setting its own. It names them in the environment and a
// cooperating extension calls `ext.Confine()`.
//
// "Cooperating" is doing real work in that sentence, and
// `Sandbox.Describe` is honest about it — the limits hold for an
// extension built to honour them and not for any other. Which makes it
// worse, not better, that the one extension this project *ships* did
// not: `sys.list_extensions` reported cpu, open-file and process limits
// as being in force on a process that had never applied them. The two
// test extensions called it, the generated skeleton emits it, and the
// shipped one was the only one nobody had cause to read.
//
// A host cannot check this — that is the whole nature of the
// arrangement — so it is checked here, where the source is.
func TestEveryShippedExtensionAppliesTheLimits(t *testing.T) {
	root := repoRoot(t)
	commands, err := filepath.Glob(filepath.Join(root, "cmd", "halite-ext-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) == 0 {
		t.Fatal("no extensions were found; this check has stopped checking")
	}

	for _, dir := range commands {
		name := filepath.Base(dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var source strings.Builder
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			source.Write(body)
		}
		if !strings.Contains(source.String(), "ext.Confine()") {
			t.Errorf("%s does not call ext.Confine(), so it runs without the resource limits "+
				"the host believes it applied, and sys.list_extensions reports them as in force",
				name)
		}
	}
	t.Logf("checked %d extension(s)", len(commands))
}

// And the skeleton the migration tool generates emits the same call, so
// a ported formula starts out honouring them rather than being the next
// thing nobody read.
func TestTheGeneratedSkeletonAppliesTheLimits(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "migrate", "skeleton.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "ext.Confine()") {
		t.Error("the generated skeleton no longer calls ext.Confine()")
	}
}
