package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A setting's documented default is the one the program falls back to.
//
// # Why this exists
//
// The key table carries a default for every setting, and
// `docs/configuration.md` is generated from it — so the reference prints
// whatever the table says, faithfully, whether or not the program agrees.
// Nothing compared the two.
//
// They disagreed. `job_queue_depth` was documented as 100 and the node
// passed 16: `cfg.Int("job_queue_depth", 16)`. An operator reading the
// reference and sizing a burst around 100 was reading a number no node has
// ever used. DIVERGENCE 5.142.
//
// # What it reads
//
// Every call to a `*Config` accessor whose second argument is a literal —
// `cfg.Int("job_queue_depth", 16)`, `cfg.String("output", "nested")`,
// `cfg.Bool("metrics", true)` — compared against the table's declared
// default for that key.
//
// A call whose default is not a literal is skipped and counted, because
// `cfg.String("pki_dir", config.DefaultPKIDir)` names a constant that the
// table's own text also names, and comparing a constant's value against
// prose would be a second copy of the mapping. Those are listed in the log
// so the gap is visible rather than implied.
func TestDocumentedDefaultsMatchTheCodesFallback(t *testing.T) {
	declared := map[string]string{}
	for _, k := range Keys {
		declared[k.Name] = k.Default
	}

	var checked, skipped int
	var skippedNames []string

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
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("%s: %v", path, parseErr)
		}
		rel, _ := filepath.Rel(root, path)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			accessor := sel.Sel.Name
			switch accessor {
			case "String", "Int", "Bool", "Float", "Duration":
			default:
				return true
			}
			// The receiver has to look like a configuration. `cfg`, `c.cfg`,
			// `n.cfg`, `h.cfg` are the spellings in this tree; anything else
			// with a two-argument String method is not this.
			if !looksLikeConfig(sel.X) {
				return true
			}
			name, ok := stringLiteral(call.Args[0])
			if !ok {
				return true
			}
			want, known := declared[name]
			if !known {
				// An unknown key is the unread-key audit's business, not
				// this one.
				return true
			}
			got, isLiteral := literalText(call.Args[1])
			if !isLiteral {
				skipped++
				skippedNames = append(skippedNames, fmt.Sprintf("%s (%s:%d)",
					name, rel, fset.Position(call.Pos()).Line))
				return true
			}
			// Two shapes that are not drift, and saying which is the
			// difference between an audit and a nuisance.
			//
			// A zero-valued fallback means "not set here": the code asks
			// for the value, gets nothing, and applies the real default
			// further down -- `cfg.String("gpg_binary", "")` beside a
			// later `if binary == "" { binary = "gpg" }`. Comparing that
			// to the table's "gpg" would flag idiom, and an audit that
			// flags idiom gets silenced.
			//
			// An empty *declared* default means the table promises
			// nothing, so whatever the call passes is a local
			// convenience rather than a documented default --
			// `cfg.String("config_file", "the configuration")`, which is
			// prose for an error message.
			if isZeroLiteral(accessor, got) || strings.TrimSpace(want) == "" {
				skipped++
				return true
			}
			checked++
			if !defaultsAgree(accessor, want, got) {
				t.Errorf("%s:%d falls back to %s for %q, and the key table declares %q.\n"+
					"\tdocs/configuration.md is generated from the table, so the reference "+
					"prints a number no program uses. Make the table match the code, or the "+
					"code match the table.",
					rel, fset.Position(call.Pos()).Line, got, name, want)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if checked == 0 {
		t.Fatal("no configuration default was compared; this audit has stopped checking")
	}
	// Both numbers, because the second is this audit's blind spot: a
	// fallback that is a constant, a zero value, or a key the table
	// declares no default for is not compared at all.
	t.Logf("compared %d literal fallback(s) against the key table; %d not compared "+
		"(a constant, a zero value, or a key with no declared default). The ones passing a "+
		"named constant: %s", checked, skipped, strings.Join(skippedNames, ", "))
}

// looksLikeConfig reports whether a receiver expression is a *Config.
func looksLikeConfig(x ast.Expr) bool {
	switch t := x.(type) {
	case *ast.Ident:
		return strings.Contains(strings.ToLower(t.Name), "cfg") ||
			strings.Contains(strings.ToLower(t.Name), "config")
	case *ast.SelectorExpr:
		return strings.Contains(strings.ToLower(t.Sel.Name), "cfg") ||
			strings.Contains(strings.ToLower(t.Sel.Name), "config")
	}
	return false
}

func stringLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// literalText renders a literal default, or reports that it is not one.
func literalText(e ast.Expr) (string, bool) {
	switch t := e.(type) {
	case *ast.BasicLit:
		if t.Kind == token.STRING {
			s, err := strconv.Unquote(t.Value)
			if err != nil {
				return "", false
			}
			return s, true
		}
		return t.Value, true
	case *ast.Ident:
		// `true` and `false` are identifiers in Go's grammar.
		if t.Name == "true" || t.Name == "false" {
			return t.Name, true
		}
	}
	return "", false
}

// isZeroLiteral reports whether a fallback is the zero value for its type,
// which in this tree means "not set here, decided further down".
func isZeroLiteral(accessor, literal string) bool {
	trimmed := strings.TrimSpace(literal)
	switch accessor {
	case "String":
		return trimmed == ""
	case "Duration":
		// A duration fallback is written either way -- `""` or a bare
		// `0` -- and both mean the same "not set here".
		if trimmed == "" {
			return true
		}
		n, err := strconv.ParseFloat(trimmed, 64)
		return err == nil && n == 0
	case "Int", "Float":
		n, err := strconv.ParseFloat(trimmed, 64)
		return err == nil && n == 0
	case "Bool":
		return trimmed == "false"
	}
	return false
}

// defaultsAgree compares a declared default with a literal fallback.
//
// The table holds strings, so a number is compared by value rather than by
// spelling: `15` and `15.0` are the same default, and so are `""` and a
// missing string.
func defaultsAgree(accessor, declared, literal string) bool {
	if declared == literal {
		return true
	}
	// A key one role reads differently from another holds both in its
	// declared default, because the table is keyed by name and `listen`
	// is `:4510` on the hub and `:4511` on the API. The reference has to
	// say both or it is wrong for one of them.
	if strings.Contains(declared, literal) {
		return true
	}
	switch accessor {
	case "Int", "Float":
		a, errA := strconv.ParseFloat(strings.TrimSpace(declared), 64)
		b, errB := strconv.ParseFloat(strings.TrimSpace(literal), 64)
		return errA == nil && errB == nil && a == b
	case "Bool":
		a, errA := strconv.ParseBool(strings.TrimSpace(declared))
		b, errB := strconv.ParseBool(strings.TrimSpace(literal))
		return errA == nil && errB == nil && a == b
	}
	return false
}
