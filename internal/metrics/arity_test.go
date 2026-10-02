package metrics_test

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

// Every `.With(...)` names one value per label its family declares.
//
// The count is checked only when the call runs, by a panic -- on
// purpose, because a series whose values do not line up with its
// labels reads as data rather than as a bug. That puts the check on the
// one path a test is least likely to take. The hub's counter for a
// failed external pillar source was called `.With("source", name)` on a
// one-label family for as long as it existed, and every external-pillar
// failure on a hub with metrics on panicked the pillar request
// (DIVERGENCE 5.196). Nothing failed until a source did.
//
// So the count is read here, statically, for every call in the tree: a
// family declared as `field: r.Counter(name, help, labels...)` or
// `x.field = r.Counter(...)` -- Histogram has buckets before its labels
// -- against every `.field.With(...)` in the same package. A `.With`
// whose receiver matches no declaration (a logger's, or a family
// declared some other way) is not checked; the floor at the end is what
// stops that from quietly becoming most of them.
func TestEveryWithNamesOneValuePerLabel(t *testing.T) {
	root, err := repotree.Root()
	if err != nil {
		t.Fatal(err)
	}
	fixedArgs := map[string]int{"Counter": 2, "Gauge": 2, "Histogram": 3}
	type call struct {
		pos   string
		field string
		n     int
	}
	labels := map[string]map[string]int{} // dir -> field -> label count
	calls := map[string][]call{}          // dir -> calls
	fset := token.NewFileSet()

	declare := func(dir, field string, c *ast.CallExpr) {
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		fixed, ok := fixedArgs[sel.Sel.Name]
		if !ok || len(c.Args) < fixed {
			return
		}
		if labels[dir] == nil {
			labels[dir] = map[string]int{}
		}
		labels[dir][field] = len(c.Args) - fixed
	}

	err = repotree.Walk(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		dir := filepath.Dir(path)
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.KeyValueExpr:
				if k, ok := x.Key.(*ast.Ident); ok {
					if c, ok := x.Value.(*ast.CallExpr); ok {
						declare(dir, k.Name, c)
					}
				}
			case *ast.AssignStmt:
				for i, rhs := range x.Rhs {
					c, ok := rhs.(*ast.CallExpr)
					if !ok || i >= len(x.Lhs) {
						continue
					}
					switch l := x.Lhs[i].(type) {
					case *ast.SelectorExpr:
						declare(dir, l.Sel.Name, c)
					case *ast.Ident:
						declare(dir, l.Name, c)
					}
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "With" {
					return true
				}
				inner, ok := sel.X.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				calls[dir] = append(calls[dir], call{fset.Position(x.Pos()).String(), inner.Sel.Name, len(x.Args)})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for dir, cs := range calls {
		for _, c := range cs {
			want, ok := labels[dir][c.field]
			if !ok {
				continue
			}
			checked++
			if c.n != want {
				t.Errorf("%s: %s.With has %d values and the family declares %d labels; it panics when it runs",
					strings.TrimPrefix(c.pos, root+string(filepath.Separator)), c.field, c.n, want)
			}
		}
	}
	// A floor, so a change in how families are declared cannot turn this
	// into a test that checks nothing and passes.
	if checked < 70 {
		t.Errorf("only %d With calls were matched to a family; this test has stopped seeing the tree", checked)
	}
	t.Logf("%d With calls matched to their families", checked)
}
