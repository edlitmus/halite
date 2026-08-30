package buildpolicy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// flagReaders are the Args methods that name a flag.
var flagReaders = map[string]bool{
	"Flag": true, "Bool": true, "Has": true,
}

// TestEveryFlagReadIsDocumented holds each binary to documenting the
// flags it reads.
//
// The programs refuse a flag no usage text describes, which turns an
// undocumented flag from something merely undiscoverable into something
// that cannot be used at all: `--principal` was read by `token revoke`
// and named nowhere, and would have started failing the moment the
// check went in. Reading the source rather than running the commands is
// what makes this cover the flags no test happens to pass.
func TestEveryFlagReadIsDocumented(t *testing.T) {
	root := repoRoot(t)
	for _, program := range []string{"halite-hub", "halite-node", "halite-api"} {
		t.Run(program, func(t *testing.T) {
			dir := filepath.Join(root, "cmd", program)
			read, documented := scanCommand(t, dir)
			for _, name := range read {
				if !documented[name] {
					t.Errorf("%s reads the flag %q and no usage text documents it, "+
						"so the program now refuses it; add it to a usage string",
						program, name)
				}
			}
		})
	}
}

// scanCommand parses a command's package and answers with the flags its
// code reads and the flags its usage strings describe.
func scanCommand(t *testing.T, dir string) (read []string, documented map[string]bool) {
	t.Helper()
	documented = map[string]bool{}
	seen := map[string]bool{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BasicLit:
				// Usage strings are raw literals; only those holding a
				// flag are usage rather than ordinary prose.
				if node.Kind == token.STRING && strings.HasPrefix(node.Value, "`") &&
					strings.Contains(node.Value, "--") {
					for flag := range cli.FlagNames(node.Value) {
						documented[flag] = true
					}
				}
			case *ast.CallExpr:
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok || !flagReaders[sel.Sel.Name] || len(node.Args) == 0 {
					return true
				}
				// Flag, Bool and Has are all names that Config and the
				// orchestration maps carry too, and those read settings
				// rather than flags. The parsed command line is `args`
				// everywhere in these packages, which distinguishes them
				// without needing the type checker.
				receiver, ok := sel.X.(*ast.Ident)
				if !ok || receiver.Name != "args" {
					return true
				}
				lit, ok := node.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				flag := strings.Trim(lit.Value, `"`)
				if flag == "" || seen[flag] {
					return true
				}
				seen[flag] = true
				read = append(read, flag)
			}
			return true
		})
	}
	return read, documented
}
