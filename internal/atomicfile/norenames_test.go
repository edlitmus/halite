package atomicfile

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/repotree"
)

// Nothing outside this package renames a path with `os.Rename`.
//
// # Why this is a rule now, when it deliberately was not
//
// `TestNothingElseWritesThroughATempFileAndARename` says in its own comment
// that it "is not a rule against `os.Rename`", and lists the eight direct calls
// as judgements rather than copies of this helper: `file.rename` and `file.move`
// doing what an operator asked, a node key moved aside before re-enrollment, a
// downloaded archive installed under its final name, the evidence log sealing a
// segment, /etc/localtime being replaced. plan.md 19f asked whether the Windows
// retry belonged at each of them.
//
// Reading `Rename` answers it for all eight at once, which the row did not
// expect:
//
//   - **On unix `atomicfile.Rename` is `os.Rename`.** Literally — one line in
//     `atomicfile_unix.go`. So on every platform this estate runs on, the
//     conversion changes nothing at all.
//   - On Windows the only difference is that it waits up to two seconds for
//     `ERROR_ACCESS_DENIED` or `ERROR_SHARING_VIOLATION`, which mean "somebody
//     has the destination open right now" and are transient by construction.
//
// The row's worry was that "a retry may hide a conflict the operator should
// see". It does not: `retry` returns the last error unwrapped when the window
// closes, so a genuinely locked file is still reported. The cost is at most two
// seconds of latency on a real denial.
//
// And at two of the eight the retry is better than latency-neutral. `file.move`
// and `movePath` both fall back to a copy when the rename fails — so without the
// retry, a Windows reader holding the destination open for a microsecond turns
// an atomic move into a copy-and-remove, losing hard links and inode identity,
// silently, and not because the operator asked for it.
//
// So there is nothing left to judge per site, and a rule is cheaper than eight
// judgements somebody has to make again. DIVERGENCE 5.165.

// renameExemptions are the direct `os.Rename` calls that stay, with the reason
// for each. Keyed by file and function, so a second one in the same file has to
// be argued for.
func renameExemptions() map[string]string {
	return map[string]string{}
}

func TestNothingElseRenamesWithTheStandardLibrary(t *testing.T) {
	exempt := renameExemptions()

	var inspected, through int
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "bin", "dist", "testdata", "contrib":
				return fs.SkipDir
			}
			// A second checkout inside this one is not this tree. See
			// internal/repotree. DIVERGENCE 5.146.
			if repotree.OtherCheckout(root, path) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// This package is the one that may: `Rename` is os.Rename on unix and
		// os.Rename in a retry loop on Windows.
		if strings.HasPrefix(rel, "internal/atomicfile/") {
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
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Rename" {
					return true
				}
				pkg, isIdent := sel.X.(*ast.Ident)
				if !isIdent {
					return true
				}
				// Counted for the log line, which is the positive half: once
				// clean this audit reports nothing, and one that had stopped
				// looking would report nothing too.
				if pkg.Name == "atomicfile" {
					through++
					return true
				}
				if pkg.Name != "os" {
					return true
				}
				key := rel + ":" + fn.Name.Name
				if reason, ok := exempt[key]; ok {
					if reason == "" {
						t.Errorf("%s is exempted with no reason", key)
					}
					return true
				}
				t.Errorf("%s:%d %s calls os.Rename. Use atomicfile.Rename, which is "+
					"os.Rename on unix and os.Rename in a short retry loop on Windows, "+
					"where MoveFileEx fails with ERROR_SHARING_VIOLATION while anybody "+
					"has the destination open -- including every os.ReadFile in the "+
					"standard library. The error is still reported when the window "+
					"closes, so nothing is hidden. If this rename genuinely wants to "+
					"fail on a reader, say so in renameExemptions. plan.md 19f, "+
					"DIVERGENCE 5.165.",
					rel, fset.Position(call.Pos()).Line, fn.Name.Name)
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if inspected < 2000 {
		t.Errorf("only %d functions were inspected; this tree has thousands, so the "+
			"walk is not reaching them", inspected)
	}
	if through < 8 {
		t.Errorf("only %d renames in this tree go through atomicfile; eight were "+
			"converted in 5.165, so either Rename has been renamed or this audit has "+
			"stopped recognising it -- and it would report nothing either way", through)
	}
	t.Logf("%d functions inspected; %d renames go through atomicfile", inspected, through)
}
