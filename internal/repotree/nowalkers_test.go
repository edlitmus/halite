package repotree

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// Nothing walks the repository with `filepath.Walk` or `filepath.WalkDir`.
//
// # Why this exists
//
// plan.md 19i: "Nothing stops a tenth repo-walking audit being written without
// the other-checkout check. A tenth written next month will read
// `.claude/worktrees/` again, reporting another commit's code as this tree's."
//
// There was already one. `internal/config`'s `TestEveryDeclaredKeyIsReadOrRecorded`
// walked `filepath.Join("..", "..")` with **no skips at all** — not `vendor`, not
// `testdata`, and not `OtherCheckout`, which every other walker was given in
// 5.146. So it read a second checkout and could count a configuration key as
// read because another commit's code read it. The row predicted next month and
// the instance predated the row.
//
// `TestNoUnitOffersAReloadThatWouldKillTheService` was the same shape with
// `OtherCheckout` and nothing else, so it read `vendor/` looking for Go that
// registers SIGHUP.
//
// # What it looks for, and the two it cannot rule on
//
// A walk whose **root expression mentions `".."` or `Root`** is reaching outside
// its own package directory, which in this repository means the repository. That
// is deliberately coarse: a precise rule would have to decide what "the
// repository" is from an arbitrary expression, and would be argued with rather
// than obeyed.
//
// It cannot see a walk that finds the root some third way. Three byte-identical
// `repoRoot(t)` helpers existed, and `Root` replaced them — so the only spellings
// left are the two above, and a fourth helper would have to be written
// deliberately. `TestNoOtherModuleRootFinder` below closes that door.
//
// **An exemption is not an exemption from `OtherCheckout`.** A walk that stays on
// `filepath.Walk` because it wants a different set of directories still has to
// skip another checkout, and this checks that it does.
//
// DIVERGENCE 5.166.

// walkExemptions are the repository walks that stay on the standard library,
// with the reason for each. Keyed by file and function.
func walkExemptions() map[string]string {
	return map[string]string{
		"internal/buildpolicy/lexicon.go:Scan": "SPEC 2.3's lexicon policy scans more " +
			"than this module's Go source -- documentation, configuration, shell and " +
			"test fixtures, all of them on purpose -- and has its own ExemptPaths list, " +
			"which already covers vendor/, .git/ and the tofu provider cache. Walk would " +
			"narrow a gate",
	}
}

// exprText renders an expression the way it was written, for matching.
func exprText(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return ""
	}
	return b.String()
}

// repoRooted reports whether a walk's root expression names the repository.
//
// # Why this needs to follow one assignment
//
// The first version matched the expression's text against `".."` and `Root`, and
// `internal/fileserver`'s `List` walks `absRoot` -- a file server root an
// operator configured, which contains the letters `Root` and is not this
// repository. A substring is not a type.
//
// Every walker in this tree writes `root := <something>` and then walks `root`,
// so following a single assignment inside the same function is enough and is
// what this does: the expression itself, or the right-hand side of an assignment
// to the identifier it uses. `repotree.Root` is matched with its package
// qualifier so that no local identifier can stand in for it.
//
// A root passed in as a parameter is not followed, and cannot be: whether
// `Scan(root)`'s argument is the repository is a property of its callers.
// `internal/buildpolicy.Scan` is that case and is exempted by name.
func repoRooted(fset *token.FileSet, body *ast.BlockStmt, arg ast.Expr) bool {
	names := func(e ast.Expr) bool {
		// `filepath.Join("..", "..", "contrib", dir)` is a **subtree** of the
		// repository and not the repository, so it is not this audit's
		// business: `readServiceFiles` reads contrib/rc.d on purpose, and the
		// first version of this flagged it and then demanded it skip a second
		// checkout that cannot be inside contrib/rc.d. A Join is the
		// repository only when every argument is `".."`.
		if call, ok := e.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Join" {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "filepath" {
					for _, a := range call.Args {
						lit, ok := a.(*ast.BasicLit)
						if !ok || lit.Value != `".."` {
							return false
						}
					}
					return len(call.Args) > 0
				}
			}
		}
		text := exprText(fset, e)
		return text == `".."` || strings.Contains(text, "repotree.Root")
	}
	if names(arg) {
		return true
	}
	id, ok := arg.(*ast.Ident)
	if !ok {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			target, ok := lhs.(*ast.Ident)
			if !ok || target.Name != id.Name || i >= len(assign.Rhs) {
				continue
			}
			if names(assign.Rhs[i]) {
				found = true
			}
		}
		return !found
	})
	return found
}

func TestNothingElseWalksTheRepository(t *testing.T) {
	exempt := walkExemptions()
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}

	var inspected, ruled, through int
	err = Walk(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel := strings.TrimPrefix(strings.ReplaceAll(strings.TrimPrefix(path, root), "\\", "/"), "/")
		// This package is what the others are told to use.
		if strings.HasPrefix(rel, "internal/repotree/") {
			return nil
		}

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("%s: %v", path, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			inspected++
			// Whether this function skips another checkout at all, for the
			// exemption check below.
			skipsOther := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "OtherCheckout" {
					skipsOther = true
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, isIdent := sel.X.(*ast.Ident)
				if !isIdent {
					return true
				}
				if pkg.Name == "repotree" && sel.Sel.Name == "Walk" {
					through++
					return true
				}
				if pkg.Name != "filepath" || (sel.Sel.Name != "Walk" && sel.Sel.Name != "WalkDir") {
					return true
				}
				// A walk rooted inside its own package is not a repository
				// walk: a test walking a t.TempDir() it built, a module
				// walking a path an operator named.
				rootExpr := exprText(fset, call.Args[0])
				if !repoRooted(fset, fn.Body, call.Args[0]) {
					return true
				}
				ruled++
				key := rel + ":" + fn.Name.Name
				if reason, ok := exempt[key]; ok {
					if reason == "" {
						t.Errorf("%s is exempted with no reason", key)
					}
					// Exempt from Walk, not from the check Walk exists for.
					if !skipsOther {
						t.Errorf("%s walks the repository with %s and is exempted from "+
							"repotree.Walk, but does not call repotree.OtherCheckout "+
							"either -- so it reads a second checkout. An exemption from "+
							"the walker is not an exemption from the rule.",
							key, sel.Sel.Name)
					}
					return true
				}
				t.Errorf("%s:%d %s walks the repository with filepath.%s, rooted at %s. "+
					"Use repotree.Walk, which skips .git, vendor, bin, dist, testdata, "+
					"contrib and any directory that is a separate git work tree. Seventeen "+
					"walkers kept six different lists, and two kept none: one read "+
					"golang.org/x/sys, and one could count a configuration key as read "+
					"because another commit's code read it. plan.md 19i, DIVERGENCE 5.166. "+
					"If this walk wants a different set of directories, say so in "+
					"walkExemptions -- and keep calling repotree.OtherCheckout.",
					rel, fset.Position(call.Pos()).Line, fn.Name.Name, sel.Sel.Name, rootExpr)
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if inspected < 2000 {
		t.Errorf("only %d functions were inspected; this tree has thousands", inspected)
	}
	if through < 15 {
		t.Errorf("only %d walks go through repotree.Walk; seventeen were converted in "+
			"5.166, so either Walk has been renamed or this audit has stopped "+
			"recognising it -- and it would report nothing either way", through)
	}
	t.Logf("%d functions inspected; %d walks go through repotree.Walk; %d "+
		"filepath walks rooted at the repository were ruled on", inspected, through, ruled)
}

// No second way to find the module root.
//
// `Root` replaced three byte-identical helpers -- `repoRoot` in
// internal/buildpolicy and internal/chaos, and `repoRootDir` in internal/exec --
// each walking up to go.mod. A fourth is how the walk audit above gets bypassed:
// a root found by a helper it cannot recognise is a root it cannot rule on.
//
// The rule is narrow on purpose: a function whose body looks for `go.mod` by
// walking up is the thing being prevented, not any function with `root` in its
// name. The two wrappers that call `Root` and add a test's `t.Fatal` are fine and
// are what this expects to find.
func TestNoOtherModuleRootFinder(t *testing.T) {
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	err = Walk(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel := strings.TrimPrefix(strings.ReplaceAll(strings.TrimPrefix(path, root), "\\", "/"), "/")
		if strings.HasPrefix(rel, "internal/repotree/") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("%s: %v", path, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			checked++
			var rendered strings.Builder
			if err := printer.Fprint(&rendered, fset, fn.Body); err != nil {
				continue
			}
			text := rendered.String()
			// Walking up to go.mod: the file name, and a step to the parent.
			if strings.Contains(text, `"go.mod"`) && strings.Contains(text, "filepath.Dir(") {
				t.Errorf("%s:%d %s walks up to go.mod. repotree.Root does that once; a "+
					"second copy is how the repository-walk audit gets bypassed, because "+
					"it cannot recognise a root found by a helper it has never heard of. "+
					"Three of these existed before 5.166.",
					rel, fset.Position(fn.Pos()).Line, fn.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 2000 {
		t.Errorf("only %d functions were checked; this tree has thousands", checked)
	}
}
