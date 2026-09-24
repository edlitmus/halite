package buildpolicy

import (
	"go/ast"
	"go/parser"
	"go/token"
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
		if !callsConfineFromMain(t, dir) {
			t.Errorf("%s does not call ext.Confine() from main, so it runs without the "+
				"resource limits the host believes it applied, and sys.list_extensions "+
				"reports them as in force", name)
		}
	}
	t.Logf("checked %d extension(s)", len(commands))
}

// callsConfineFromMain reports whether an extension's `main` actually
// calls it.
//
// Parsed rather than searched. This was `strings.Contains(source,
// "ext.Confine()")`, and a commented-out call contains that string --
// so the defect the check was written for reinstates by putting `//` in
// front of the line it was written about. That is the shape this
// project's audits keep being caught by: a text search standing in for a
// check. DIVERGENCE 5.138.
//
// From `main` specifically, because the limits bound the calling process
// and are useless applied somewhere that never runs: a call in a helper
// nobody invokes reads identically to a call that happens.
func callsConfineFromMain(t *testing.T, dir string) bool {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Name.Name != "main" || fn.Body == nil {
					continue
				}
				found := false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Confine" {
						return true
					}
					if pkgIdent, ok := sel.X.(*ast.Ident); ok && pkgIdent.Name == "ext" {
						found = true
						return false
					}
					return true
				})
				if found {
					return true
				}
			}
		}
	}
	return false
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
