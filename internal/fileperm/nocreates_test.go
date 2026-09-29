package fileperm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/repotree"
)

// Nothing outside this package makes a private file or directory with the
// standard library alone.
//
// # Why this exists
//
// `os.MkdirAll(dir, 0o700)` and `os.WriteFile(path, data, 0o600)` are how a Go
// program says "only this account", and on Windows they say nothing: a
// directory mode is not an access control decision there, and a file mode is
// the read-only attribute. Thirty-one places in this repository said it that
// way, each one line of intent the platform discarded.
//
// They were all converted at once, and the argument for the audit rather than
// for the thirty-one fixes is what the last two rounds established. 5.144
// consolidated six copies of `atomicfile.Write`, and three more were written
// afterwards. 5.161 found that `internal/pki` — the package holding the
// enrollment CA's private key, with more reason to care than any other — used
// this package nowhere, a year after it was written for exactly that.
//
// Nobody wrote any of those carelessly. Each was correct on unix, which is
// where it was read.
//
// # What it looks for, and what it cannot
//
// A call to `os.MkdirAll`, `os.WriteFile` or `os.OpenFile` whose mode is a
// **literal that denies group and other**. The test for that is
// `mode&0o077 == 0`, which is the same expression `Apply` uses to decide
// whether the platform needs more than a chmod — one rule, not two that have
// to be kept in step.
//
// A mode that is not a literal is skipped, and there is no way round that:
// `os.MkdirAll(target, os.FileMode(h.Mode).Perm()|0o700)` in the tar extractor
// takes its mode from the archive, and whether that denies group depends on the
// archive. Stated rather than silently passed over, because it means this audit
// bounds the problem and does not close it.
//
// **An `os.OpenFile` without `O_CREATE` is not ruled on**, because its mode
// argument is not a mode: Go ignores it unless the call creates the file, and
// the convention is to pass `0`. The first version of this audit did not know
// that and reported `accessible(path, flag)` in `file_more.go`, which opens a
// path to find out whether it can be opened — `0` satisfied `mode&0o077 == 0`
// and the finding was about nothing. Two of the first seventeen findings were
// that mistake, which is the argument for reading a whole list before acting on
// any of it.
//
// A public mode is not this audit's business. `0o755` on a directory netplan
// reads, `0o644` on a certificate — a certificate is public and the difference
// from a key is the whole point of this package. Those are left alone.
//
// DIVERGENCE 5.164.

// createExemptions are the private-mode calls that stay on the standard
// library, with the reason for each. Keyed by file and function, so a second
// one in the same file has to be argued for.
func createExemptions() map[string]string {
	return map[string]string{}
}

// privateModeLiteral reports the mode a call asks for, when it is a literal
// this audit can rule on.
//
// The mode is the last argument of MkdirAll and WriteFile and the third of
// OpenFile, which is to say it is always the last one — so the position is not
// hardcoded per function and a signature nobody here expected cannot be read
// off the wrong argument.
func privateModeLiteral(call *ast.CallExpr) (mode uint64, isLiteral bool) {
	if len(call.Args) == 0 {
		return 0, false
	}
	last := call.Args[len(call.Args)-1]
	lit, ok := last.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	n, err := strconv.ParseUint(lit.Value, 0, 32)
	if err != nil {
		return 0, false
	}
	return n, true
}

// createsFile reports whether an os.OpenFile call can create the file, which is
// the only case in which its mode argument means anything.
//
// Read out of the flag expression rather than inferred from the mode: a call
// that passes 0 because it is not creating anything is indistinguishable, by
// mode alone, from one asking for a file nobody can read.
func createsFile(call *ast.CallExpr) bool {
	if len(call.Args) < 2 {
		return false
	}
	found := false
	ast.Inspect(call.Args[1], func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "O_CREATE" {
			found = true
		}
		return !found
	})
	return found
}

func TestNothingElseCreatesAPrivateFileWithTheStandardLibrary(t *testing.T) {
	exempt := createExemptions()
	// The calls this audit rules on, and the call in this package that each
	// should go through instead.
	replacement := map[string]string{
		"MkdirAll":  "fileperm.MkdirAll",
		"WriteFile": "fileperm.WriteFile",
		"OpenFile":  "fileperm.OpenFile",
	}

	var inspected, ruled, through int
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
			// A second checkout inside this one is not this tree:
			// `.claude/worktrees/` holds one at another commit, and this
			// walker would otherwise report its contents as findings
			// against this tree. See internal/repotree. DIVERGENCE 5.146.
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
		switch {
		case strings.HasPrefix(rel, "internal/fileperm/"):
			// This package is what the others are told to use.
			return nil
		case strings.HasPrefix(rel, "internal/atomicfile/"):
			// atomicfile restricts its temporary file through ApplyFile
			// before the rename, which is stricter than these helpers: the
			// file is private before it has its final name. 5.144.
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
				if !ok {
					return true
				}
				pkg, isIdent := sel.X.(*ast.Ident)
				if !isIdent {
					return true
				}
				instead, interesting := replacement[sel.Sel.Name]
				if !interesting {
					return true
				}
				// Counted for the log line below, which is the positive
				// half: once this audit is clean it reports nothing, and a
				// clean audit that had stopped looking would report nothing
				// too. The number of calls that *do* go through this package
				// is what tells the two apart.
				if pkg.Name == "fileperm" {
					through++
					return true
				}
				if pkg.Name != "os" {
					return true
				}
				if sel.Sel.Name == "OpenFile" && !createsFile(call) {
					return true
				}
				mode, isLiteral := privateModeLiteral(call)
				if !isLiteral || mode&0o077 != 0 {
					return true
				}
				ruled++
				key := rel + ":" + fn.Name.Name
				if reason, ok := exempt[key]; ok {
					if reason == "" {
						t.Errorf("%s is exempted with no reason", key)
					}
					return true
				}
				t.Errorf("%s:%d %s calls os.%s with mode %#o, which denies group and "+
					"other — and on Windows says nothing at all: a directory mode is not "+
					"an access control decision there and a file mode is the read-only "+
					"attribute. Use %s, which carries the intent out on both platforms. "+
					"internal/pki said it the standard library's way for a year, about the "+
					"enrollment CA's private key (DIVERGENCE 5.161). If this path genuinely "+
					"does not need it, say so in createExemptions.",
					rel, fset.Position(call.Pos()).Line, fn.Name.Name,
					sel.Sel.Name, mode, instead)
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The audit has to have looked at something. A walk whose filters
	// excluded the whole tree passes silently, which is how a gate becomes
	// decoration -- 5.124's lesson about a leg whose filter matched nothing.
	if inspected < 2000 {
		t.Errorf("only %d functions were inspected; this tree has thousands, so the "+
			"walk is not reaching them", inspected)
	}
	if through < 20 {
		t.Errorf("only %d calls in this tree go through fileperm; thirty-one were "+
			"converted in 5.164, so either the helpers have been renamed or this "+
			"audit has stopped recognising them -- and it would report nothing either "+
			"way", through)
	}
	t.Logf("%d functions inspected; %d calls go through fileperm; %d os.* calls with a "+
		"private mode literal were ruled on", inspected, through, ruled)
}
