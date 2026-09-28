package builtin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/states"
)

// No state's comment may open with a lower-case English word.
//
// # Why this is a static check and not a conformance case
//
// SPEC 11.6 says a state's comment must be a human sentence, and
// `states.CommentIsASentence` is the rule: an upper-case letter, a digit, an
// identifier or path, or a backtick-quoted literal. What it refuses is a
// comment that opens with a lower-case English word, which is either a
// fragment ("changed", "done") or a sentence started in the middle ("the rule
// was removed").
//
// The harness enforces it, and the harness only reaches a function that has a
// case. So the rule has been discovered four separate times, each on a
// **billed lab instance**, each time for one more module family: `beacon` and
// `schedule`, then `nftables`, then `iptables`, then `lvm`. Every time the
// answer was the same mechanical edit, and every time it cost a lab cycle to
// find out.
//
// This asks the question of every state comment in the package at
// `make check` time. Thirteen more were waiting -- in `iptables`, `mdadm`,
// `tls` and `udev` -- for whenever their first case arrived.
//
// # What it can and cannot judge
//
// It reads *format strings*, so it judges only a literal prefix. A comment
// opening with `%s` or `%d` becomes a path, a device or a name at runtime,
// which the rule admits and this check cannot evaluate -- 289 constructions in
// this package are that shape, and they stay the harness's business. What
// remains is the prefix somebody typed, which is exactly where the mistake
// keeps being made.
//
// DIVERGENCE 5.157.
func TestNoStateCommentOpensWithALowerCaseWord(t *testing.T) {
	root := repoRootForACLCheck(t)
	dir := filepath.Join(root, "internal", "builtin")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	checked, judged := 0, 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		checked++

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isStateResultCall(call) {
				return true
			}
			for _, arg := range call.Args {
				format, ok := sprintfFormat(arg)
				if !ok {
					continue
				}
				// Only a literal prefix can be judged here. A format
				// opening with a substitution is the harness's to check
				// at runtime, against the value that lands there.
				if format == "" || !isLowerASCIILetter(rune(format[0])) {
					continue
				}
				judged++
				if err := states.CommentIsASentence(format); err != nil {
					t.Errorf("%s:%d opens a state comment with a lower-case word: %q\n"+
						"  %v\n"+
						"  SPEC 11.6 wants a human sentence, and the conformance harness "+
						"refuses this the day a case for the function arrives -- which has "+
						"cost four lab runs to discover one family at a time.",
						e.Name(), fset.Position(arg.Pos()).Line, format, err)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no Go files were parsed, so this audit checked nothing")
	}
	t.Logf("read %d files; judged %d comment formats with a literal lower-case opening",
		checked, judged)
}

// isStateResultCall reports whether this call builds a state's result, and so
// carries a comment an operator reads.
func isStateResultCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		// states.Changed, states.WouldChange, states.True, states.False.
		pkg, ok := fn.X.(*ast.Ident)
		if !ok || pkg.Name != "states" {
			return false
		}
		switch fn.Sel.Name {
		case "Changed", "WouldChange", "True", "False":
			return true
		}
	case *ast.Ident:
		// The per-module helpers: nftMutateResult, iptablesMutateResult,
		// lvmMutateResult, aclMutateResult and their kind. Matched by shape
		// rather than by a list, so a new one is covered the day it exists.
		return strings.HasSuffix(fn.Name, "MutateResult")
	}
	return false
}

// sprintfFormat returns the format string of an fmt.Sprintf call, or the
// literal itself when the argument is a plain string.
func sprintfFormat(arg ast.Expr) (string, bool) {
	switch a := arg.(type) {
	case *ast.BasicLit:
		if a.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(a.Value)
		return v, err == nil
	case *ast.CallExpr:
		sel, ok := a.Fun.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "fmt" || sel.Sel.Name != "Sprintf" {
			return "", false
		}
		if len(a.Args) == 0 {
			return "", false
		}
		lit, ok := a.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(lit.Value)
		return v, err == nil
	}
	return "", false
}

func isLowerASCIILetter(r rune) bool { return r >= 'a' && r <= 'z' }
