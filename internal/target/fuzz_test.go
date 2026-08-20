package target

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The compound target grammar is the parser most exposed to operator
// typing, and SPEC section 31 names it as a fuzz target. A panic in it is
// reachable from any command line.
func FuzzCompileAuto(f *testing.F) {
	seeds := []string{
		"*",
		"web*.prod",
		"G@os_family:FreeBSD",
		"E@^(web|db)[0-9]{2}$",
		"P@os:(Free|Open)BSD",
		"L@web1,web2,web3",
		"S@10.0.0.0/8",
		"N@webservers",
		"I@role:web",
		"J@role:^web",
		"R@user@host",
		"G@os:FreeBSD and not G@virtual:physical",
		"web* or (G@os:Ubuntu and not L@web1)",
		"not not not *",
		"( ( ( * ) ) )",
		"and",
		"or or",
		"@",
		"G@",
		"@:",
		"E@(",
		"E@(?=lookahead)",
		strings.Repeat("not ", 200) + "*",
		strings.Repeat("(", 200) + "*" + strings.Repeat(")", 200),
		strings.Repeat("* and ", 200) + "*",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	node := Node{
		ID:     "web1.prod.example.com",
		Grains: value.MapOf("os", "FreeBSD", "os_family", "FreeBSD", "roles", []any{"web"}),
		Pillar: value.MapOf("role", "web"),
	}

	f.Fuzz(func(t *testing.T, expr string) {
		m, err := CompileAuto(expr, nil)
		if err != nil {
			if m != nil {
				t.Errorf("a failed compile returned a matcher as well as %v", err)
			}
			return
		}
		if m == nil {
			t.Fatal("CompileAuto returned neither a matcher nor an error")
		}
		// Matching must not panic and must be a pure predicate: the same
		// node twice gives the same answer.
		first := m.Match(node)
		if second := m.Match(node); first != second {
			t.Errorf("%q matched %v then %v against the same node", expr, first, second)
		}
		if m.Expr() == "" && expr != "" {
			t.Errorf("%q compiled to a matcher that reports no expression", expr)
		}
	})
}

func FuzzCompileKind(f *testing.F) {
	f.Add(byte(0), "web*")
	for k := Kind(0); k < 12; k++ {
		f.Add(byte(k), "value")
		f.Add(byte(k), "a:b")
		f.Add(byte(k), "^(")
	}
	node := Node{ID: "web1", Grains: value.MapOf("os", "FreeBSD"), Pillar: value.NewMap(0)}
	f.Fuzz(func(t *testing.T, kind byte, expr string) {
		m, err := Compile(Kind(kind), expr, nil)
		if err != nil {
			return
		}
		if m == nil {
			t.Fatal("Compile returned neither a matcher nor an error")
		}
		m.Match(node)
	})
}
