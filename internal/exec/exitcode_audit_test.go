package exec_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/repotree"
)

// TestEveryExitCodeReadAsksForIt holds every caller to the contract
// `IgnoreExitCode` actually has.
//
// # The defect this is here for
//
// `OSRunner` turns a non-zero exit into a Go error unless the command
// sets `IgnoreExitCode`. So a caller that writes
//
//	res, err := c.Run(exec.Command{Argv: argv})
//	if err != nil {
//		return err
//	}
//	if res.Code != 0 {
//		return fmt.Errorf("...")   // unreachable
//	}
//
// has written a branch that cannot run against a real machine. `c.Run`
// returned the error first.
//
// `mac_defaults` did this at all four of its call sites, and the branch
// it made unreachable was the one that treated "there was nothing to
// delete" as success — so `mac_defaults.delete` on a key that was
// already gone was an error, which is the opposite of what the function
// documented. DIVERGENCE 5.114. The sweep that followed found five more
// in `mac_power` and `ps`, where the cost was smaller but the same
// shape: the module's own error message, which names the tool and reads
// both streams, lost to the runner's generic one.
//
// # Why a test and not a review habit
//
// `RecordingRunner` hands back the scripted `Result` — exit code and all
// — with a nil error, which is what a unit test sees. `OSRunner` returns
// an error, which is what a machine sees. The two disagree exactly here,
// so a module with this defect passes its whole test file and fails on
// hardware. No fixture can catch it; the source can.
//
// A caller that genuinely wants the runner's error and never reads
// `Code` is untouched by this. The rule is only that reading the code
// means asking for it.
func TestEveryExitCodeReadAsksForIt(t *testing.T) {
	root := repoRootDir(t)
	var problems []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "vendor", "testdata", ".git":
				return filepath.SkipDir
			}
			if repotree.OtherCheckout(root, path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		problems = append(problems, scanFileForBlindExitCodeReads(t, path, root)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	sort.Strings(problems)
	for _, p := range problems {
		t.Errorf("%s\n\tthis reads the command's exit code, but the command does not set "+
			"IgnoreExitCode, so OSRunner has already returned an error and the branch "+
			"cannot run on a real machine (DIVERGENCE 5.114)", p)
	}
}

// scanFileForBlindExitCodeReads reports the `<var>.Code` reads in one
// file whose command never asked to see a non-zero exit.
func scanFileForBlindExitCodeReads(t *testing.T, path, root string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// A file this package cannot parse is not this test's business;
		// the build says so far more clearly.
		return nil
	}

	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		asked := resultsThatAskedForTheirExitCode(fn.Body)
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			sel, ok := m.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Code" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ignored, tracked := asked[ident.Name]; !tracked || ignored {
				return true
			}
			rel, relErr := filepath.Rel(root, fset.Position(sel.Pos()).Filename)
			if relErr != nil {
				rel = path
			}
			out = append(out, fmt.Sprintf("%s:%d: %s() reads %s.Code",
				rel, fset.Position(sel.Pos()).Line, fn.Name.Name, ident.Name))
			return true
		})
		return true
	})
	return out
}

// resultsThatAskedForTheirExitCode maps each variable assigned from a
// `Run`/`RunArgv` call to whether that call set `IgnoreExitCode`.
//
// A variable that is not in the map came from somewhere this test
// cannot see, and is left alone: the rule is worth having only while a
// failure means what it says.
func resultsThatAskedForTheirExitCode(body *ast.BlockStmt) map[string]bool {
	asked := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || len(as.Lhs) == 0 {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name, ok := as.Lhs[0].(*ast.Ident)
		if !ok || name.Name == "_" {
			return true
		}

		switch sel.Sel.Name {
		case "RunArgv":
			// RunArgv builds the Command itself and cannot set the flag,
			// so its exit code is never the caller's to read.
			asked[name.Name] = false
		case "Run":
			if len(call.Args) != 1 {
				return true
			}
			lit, ok := call.Args[0].(*ast.CompositeLit)
			if !ok {
				// The Command was built elsewhere; this test cannot say.
				asked[name.Name] = true
				return true
			}
			asked[name.Name] = compositeSetsIgnoreExitCode(lit)
		}
		return true
	})
	return asked
}

// compositeSetsIgnoreExitCode reports whether a Command literal asks to
// see a non-zero exit. A value that is not the literal `false` counts as
// asking, because a flag decided at runtime is not this test's to judge.
func compositeSetsIgnoreExitCode(lit *ast.CompositeLit) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "IgnoreExitCode" {
			continue
		}
		if ident, ok := kv.Value.(*ast.Ident); ok && ident.Name == "false" {
			continue
		}
		return true
	}
	return false
}

func repoRootDir(t *testing.T) string {
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
