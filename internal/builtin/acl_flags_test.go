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
)

// Nothing in this package may pass `-s` to getfacl.
//
// # Why a tree-wide check rather than a test of one function
//
// `getfacl -s` was added in FreeBSD 15. On 14 it is not an option at all --
// the usage is `getfacl [-dhnqv]` -- so the call fails with
//
//	getfacl: illegal option -- s
//
// for every path, on a platform that is SPEC 27.1 tier 1 and carries about
// 80% of this estate. DIVERGENCE 5.113 recorded that, and 5.113 was fixed:
// `aclExtendedMark` exists precisely to answer "is this ACL extended?"
// without the flag, and carries twenty lines explaining why.
//
// It was fixed in one place. `acl.wipe` kept calling `getfacl -sq` three
// hundred and ninety lines below that explanation, and so was unusable on
// FreeBSD 14 -- found when the live ACL test failed there in three
// consecutive lab runs, having previously been read as a fact about the
// machine rather than about the code.
//
// A comment cannot stop that happening again and a test of `acl.wipe` would
// only cover `acl.wipe`. This asks the question of the whole package, which
// is the level the mistake was made at: the knowledge was present, in prose,
// and the second call site simply never met it.
func TestNothingPassesTheFreeBSD15OnlyFlagToGetfacl(t *testing.T) {
	root := repoRootForACLCheck(t)
	dir := filepath.Join(root, "internal", "builtin")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
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
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			strs := make([]string, 0, len(lit.Elts))
			for _, el := range lit.Elts {
				bl, ok := el.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(bl.Value); err == nil {
					strs = append(strs, v)
				}
			}
			// Only a list that runs getfacl is this test's business. An
			// `-s` elsewhere is somebody else's flag: `dpkg-query -s` and
			// `iptables -s` are both correct and both in this package.
			if !aclArgvRunsGetfacl(strs) {
				return true
			}
			for _, s := range strs {
				if s == "-s" || (strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "--") &&
					strings.Contains(s, "s") && isShortFlagCluster(s)) {
					t.Errorf("%s:%d passes %q to getfacl. That flag arrived in FreeBSD 15 "+
						"and does not exist on 14, which is tier 1; ask aclExtendedMark "+
						"instead, which is what it is for. DIVERGENCE 5.113.",
						e.Name(), fset.Position(lit.Pos()).Line, s)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no Go files were parsed, so this audit checked nothing")
	}
	t.Logf("checked %d files in internal/builtin for getfacl -s", checked)
}

// isShortFlagCluster reports whether a flag is a bundle of short options,
// such as `-sq`, rather than a long one or a value. `-s` hidden inside a
// cluster is the form that was actually shipped.
func isShortFlagCluster(s string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	for _, r := range s[1:] {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// repoRootForACLCheck walks up to the directory holding go.mod.
func repoRootForACLCheck(t *testing.T) string {
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
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// aclArgvRunsGetfacl reports whether this argument list runs getfacl.
func aclArgvRunsGetfacl(argv []string) bool {
	for _, s := range argv {
		if s == "getfacl" {
			return true
		}
	}
	return false
}
