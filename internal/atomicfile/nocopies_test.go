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

// Nothing outside this package writes a file by hand through a temporary
// file and a rename.
//
// # Why this exists
//
// This package's own documentation opens by saying that six packages had
// their own copy of the same helper and all six were wrong on Windows.
// They were consolidated. Nothing stopped a seventh, and three were found
// afterwards:
//
//   - `internal/keystore.writeAtomic`, which had to do one extra thing
//     before the rename and so kept the whole helper to do it;
//   - `internal/pki.writeFile`, in the package that holds the enrollment
//     CA;
//   - `cmd/halite-node.writeFileAtomic`, for a node's own runtime
//     configuration.
//
// All three had drifted in the same three ways, which is the argument for
// the audit rather than for the three fixes: they chmod'd the temporary
// file directly instead of going through internal/fileperm, so a mode on
// Windows was the read-only attribute and no access control list; two of
// the three called `os.Rename` rather than `Rename` here, so the Windows
// sharing race this package exists for applied to them; and none of the
// three synced the directory after the rename, so a power loss could
// leave a file with no name.
//
// Nobody wrote those three copies carelessly. Each was written before or
// beside this package and each was correct on unix, which is where they
// were read.
//
// # What it looks for
//
// A function that both makes a temporary file and renames something is
// doing this package's job. That is deliberately a coarse rule, because a
// precise one would have to decide what counts as "atomic enough" and
// would be argued with rather than obeyed.
//
// It is not a rule against `os.Rename`. There are nine other direct
// renames in this tree -- `file.rename` and `file.move` doing what an
// operator asked, a key being moved aside before re-enrollment, a
// downloaded archive being installed under its final name, the evidence
// log sealing a segment -- and each of those is a judgement about whether
// the Windows retry belongs there, not a duplicate of this helper. They
// are recorded in plan.md rather than changed here.
//
// DIVERGENCE 5.144.
// exemptions are the functions that pair a temporary file with a rename
// and are not a copy of this helper, with the reason for each.
//
// A streamed download cannot go through Write, which takes the whole body
// as a []byte: an archive is extracted from a path and is never meant to
// be in memory. Naming the function rather than the file means a second
// one in the same file has to be argued for.
func exemptions() map[string]string {
	return map[string]string{
		"internal/builtin/archive_remote.go:fetchRemoteArchive": "streams a remote archive to " +
			"disk and installs it under its final name; Write takes the whole body as a []byte " +
			"and the estate's own case is a JDK tarball",
	}
}

func TestNothingElseWritesThroughATempFileAndARename(t *testing.T) {
	exempt := exemptions()

	var inspected int
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
			// walker would otherwise read it and report its contents as
			// findings against this tree. See internal/repotree.
			// DIVERGENCE 5.146.
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
			makesTemp, renames := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "CreateTemp", "MkdirTemp":
					makesTemp = true
				case "Rename":
					renames = true
				}
				return true
			})
			if !makesTemp || !renames {
				continue
			}
			key := rel + ":" + fn.Name.Name
			if reason, ok := exempt[key]; ok {
				if reason == "" {
					t.Errorf("%s is exempted with no reason", key)
				}
				continue
			}
			t.Errorf("%s:%d %s makes a temporary file and renames it, which is "+
				"atomicfile.Write's job. Three copies of this helper were found after the "+
				"first six were consolidated, and all three had lost internal/fileperm, the "+
				"Windows rename retry, or the directory sync. Call atomicfile.Write, or "+
				"atomicfile.WritePrepared if a step has to happen before the rename; if it "+
				"genuinely cannot, add it to this test's `exempt` map with the reason.",
				rel, fset.Position(fn.Pos()).Line, fn.Name.Name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspected == 0 {
		t.Fatal("no function was inspected; this audit has stopped checking anything")
	}
	t.Logf("inspected %d function(s) outside this package", inspected)
}

// Nothing is exempted that is not a function pairing a temp file with a
// rename, so that an exemption cannot outlive what it excused.
func TestNoExemptionOutlivesItsFunction(t *testing.T) {
	// Read the same way the audit reads, so the two cannot disagree about
	// what a match is.
	present := map[string]bool{}
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
			// walker would otherwise read it and report its contents as
			// findings against this tree. See internal/repotree.
			// DIVERGENCE 5.146.
			if repotree.OtherCheckout(root, path) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				present[filepath.ToSlash(rel)+":"+fn.Name.Name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range exemptions() {
		if !present[key] {
			t.Errorf("%s is exempted from the temp-file-and-rename audit and no such "+
				"function exists. An exemption left behind covers whatever takes that "+
				"name next.", key)
		}
	}
}
