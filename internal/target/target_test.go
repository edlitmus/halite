package target

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

func web1() Node {
	return Node{
		ID: "web1.prod",
		Grains: value.MapOf(
			"os", "Ubuntu",
			"os_family", "Debian",
			"osrelease", "22.04",
			"roles", []any{"web", "cache"},
			"ipv4", []any{"127.0.0.1", "10.0.1.15"},
			"ipv6", []any{"::1"},
			"nested", value.MapOf("deep", "value"),
		),
		Pillar: value.MapOf(
			"role", "webserver",
			"tier", "frontend",
		),
	}
}

func db1() Node {
	return Node{
		ID: "db1.prod",
		Grains: value.MapOf(
			"os", "Rocky",
			"os_family", "RedHat",
			"osrelease", "9.3",
			"roles", []any{"db"},
			"ipv4", []any{"10.0.2.20"},
		),
		Pillar: value.MapOf("role", "database"),
	}
}

func mustMatch(t *testing.T, kind Kind, expr string, n Node, want bool) {
	t.Helper()
	m, err := Compile(kind, expr, nil)
	if err != nil {
		t.Fatalf("compiling %q: %v", expr, err)
	}
	if got := m.Match(n); got != want {
		t.Errorf("%q against %s = %v, want %v", expr, n.ID, got, want)
	}
}

func TestGlobTargeting(t *testing.T) {
	mustMatch(t, Glob, "web*", web1(), true)
	mustMatch(t, Glob, "web*.prod", web1(), true)
	mustMatch(t, Glob, "*", web1(), true)
	mustMatch(t, Glob, "db*", web1(), false)
	mustMatch(t, Glob, "web1.prod", web1(), true)
	// A partial match is not a match.
	mustMatch(t, Glob, "web1", web1(), false)
}

func TestListTargeting(t *testing.T) {
	mustMatch(t, List, "web1.prod,db1.prod", web1(), true)
	mustMatch(t, List, "web2.prod, db1.prod", web1(), false)
	mustMatch(t, List, " web1.prod ", web1(), true)
}

func TestRegexTargeting(t *testing.T) {
	mustMatch(t, Regex, `^web[0-9]+\.prod$`, web1(), true)
	mustMatch(t, Regex, `^db`, web1(), false)
}

func TestRegexRefusesUnsupportedConstructs(t *testing.T) {
	// A ReDoS-safe targeting engine is a security property worth keeping,
	// so this is permanently RE2.
	_, err := Compile(Regex, `web(?=1)`, nil)
	if err == nil {
		t.Fatal("a lookahead in a target expression must be refused")
	}
	if !strings.Contains(err.Error(), "lookahead") {
		t.Errorf("the error should name the construct: %v", err)
	}
}

func TestGrainTargeting(t *testing.T) {
	mustMatch(t, Grain, "os_family:Debian", web1(), true)
	mustMatch(t, Grain, "os_family:RedHat", web1(), false)
	mustMatch(t, Grain, "osrelease:22.*", web1(), true)
	mustMatch(t, Grain, "nested:deep:value", web1(), true)
	// A list-valued grain matches on any member.
	mustMatch(t, Grain, "roles:cache", web1(), true)
	mustMatch(t, Grain, "roles:db", web1(), false)
	// A grain name with no value matches its presence.
	mustMatch(t, Grain, "os_family", web1(), true)
	mustMatch(t, Grain, "no_such_grain", web1(), false)
}

func TestGrainRegexTargeting(t *testing.T) {
	mustMatch(t, GrainRegex, `osrelease:^22\.`, web1(), true)
	mustMatch(t, GrainRegex, `osrelease:^9\.`, web1(), false)
	mustMatch(t, GrainRegex, `roles:^ca`, web1(), true)
}

func TestPillarTargeting(t *testing.T) {
	mustMatch(t, Pillar, "role:webserver", web1(), true)
	mustMatch(t, Pillar, "role:database", web1(), false)
	mustMatch(t, PillarRegex, "role:^web", web1(), true)
}

func TestSubnetTargeting(t *testing.T) {
	mustMatch(t, Subnet, "10.0.0.0/8", web1(), true)
	mustMatch(t, Subnet, "10.0.2.0/24", web1(), false)
	mustMatch(t, Subnet, "10.0.1.15", web1(), true)
	mustMatch(t, Subnet, "10.0.1.16", web1(), false)
	mustMatch(t, Subnet, "::1/128", web1(), true)
}

func TestSubnetRejectsNonsense(t *testing.T) {
	if _, err := Compile(Subnet, "not-an-address", nil); err == nil {
		t.Error("a malformed subnet must be an error, not a match-nothing")
	}
}

func TestCompoundPrecedence(t *testing.T) {
	cases := []struct {
		expr string
		web  bool
		db   bool
	}{
		{"G@os_family:Debian", true, false},
		{"G@os_family:Debian and web*", true, false},
		{"G@os_family:Debian or G@os_family:RedHat", true, true},
		{"not G@os_family:Debian", false, true},
		{"G@os_family:Debian and not web5*", true, false},
		// not binds tighter than and, which binds tighter than or.
		{"not web* and db*", false, true},
		{"web* or db* and G@os_family:RedHat", true, true},
		{"(web* or db*) and G@os_family:Debian", true, false},
		{"I@role:webserver and S@10.0.0.0/8", true, false},
		{"L@web1.prod,db1.prod and not I@role:database", true, false},
		{"E@^web and P@osrelease:^22", true, false},
	}
	for _, c := range cases {
		mustMatch(t, Compound, c.expr, web1(), c.web)
		mustMatch(t, Compound, c.expr, db1(), c.db)
	}
}

func TestCompoundErrorsNameTokenAndColumn(t *testing.T) {
	cases := []struct {
		expr    string
		wantCol int
		wantMsg string
	}{
		{"web* and", 9, "ends where a target was expected"},
		{"and web*", 1, "needs a target on its left"},
		{"( web*", 1, "never closed"},
		{"web* )", 6, "unmatched"},
		{"", 0, "empty"},
		{"G@", 1, "has no value"},
	}
	for _, c := range cases {
		_, err := Compile(Compound, c.expr, nil)
		if err == nil {
			t.Errorf("%q should be an error", c.expr)
			continue
		}
		te, ok := err.(*Error)
		if !ok {
			t.Errorf("%q gave %T, want a target error", c.expr, err)
			continue
		}
		if !strings.Contains(te.Msg, c.wantMsg) {
			t.Errorf("%q: message %q does not contain %q", c.expr, te.Msg, c.wantMsg)
		}
		if c.wantCol > 0 && te.Col != c.wantCol {
			t.Errorf("%q: column %d, want %d", c.expr, te.Col, c.wantCol)
		}
	}
}

// TestBadExpressionNeverWidens is the property that matters: a malformed
// expression must fail, never degrade into a broader match.
func TestBadExpressionNeverWidens(t *testing.T) {
	for _, expr := range []string{"web* and", "( web*", "G@", "E@(?=x)"} {
		m, err := Compile(Compound, expr, nil)
		if err == nil {
			t.Errorf("%q compiled; it must not", expr)
			continue
		}
		if m != nil {
			t.Errorf("%q returned a matcher alongside its error", expr)
		}
	}
}

func TestNodegroups(t *testing.T) {
	groups := Nodegroups{
		"prod_web": "G@os_family:Debian and web*",
		"prod_db":  "db*.prod",
		"prod_all": "N@prod_web or N@prod_db",
	}
	mustMatchGroup := func(expr string, n Node, want bool) {
		t.Helper()
		m, err := Compile(Nodegroup, expr, groups)
		if err != nil {
			t.Fatalf("compiling nodegroup %q: %v", expr, err)
		}
		if got := m.Match(n); got != want {
			t.Errorf("nodegroup %q against %s = %v, want %v", expr, n.ID, got, want)
		}
	}
	mustMatchGroup("prod_web", web1(), true)
	mustMatchGroup("prod_web", db1(), false)
	mustMatchGroup("prod_all", web1(), true)
	mustMatchGroup("prod_all", db1(), true)

	// N@ inside a compound expression resolves too.
	m, err := Compile(Compound, "N@prod_web and not G@os_family:RedHat", groups)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match(web1()) {
		t.Error("N@ inside a compound expression did not resolve")
	}
}

// TestNodegroupCycleIsCaughtAtLoad covers SPEC section 8.2: a cycle is a
// configuration error detected at load rather than at use.
func TestNodegroupCycleIsCaughtAtLoad(t *testing.T) {
	groups := Nodegroups{
		"a": "N@b",
		"b": "N@a",
	}
	err := ValidateNodegroups(groups)
	if err == nil {
		t.Fatal("a nodegroup cycle must be refused at load")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("the error should name the cycle: %v", err)
	}
}

func TestNodegroupDepthIsBounded(t *testing.T) {
	groups := Nodegroups{}
	for i := 0; i < 15; i++ {
		groups[string(rune('a'+i))] = "N@" + string(rune('a'+i+1))
	}
	groups[string(rune('a'+15))] = "web*"
	if err := ValidateNodegroups(groups); err == nil {
		t.Error("nodegroup nesting past the limit must be refused")
	}
}

func TestUndefinedNodegroup(t *testing.T) {
	_, err := Compile(Nodegroup, "nope", Nodegroups{})
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Errorf("err = %v", err)
	}
}

func TestValidateNodegroupsAcceptsAGoodSet(t *testing.T) {
	groups := Nodegroups{
		"web": "web*",
		"db":  "db*",
		"all": "N@web or N@db",
	}
	if err := ValidateNodegroups(groups); err != nil {
		t.Errorf("a valid nodegroup set was refused: %v", err)
	}
}

func TestCompileAutoPicksTheGrammar(t *testing.T) {
	// A top file entry is a bare glob unless it carries compound syntax.
	m, err := CompileAuto("web*", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match(web1()) || m.Match(db1()) {
		t.Error("a bare glob did not behave as a glob")
	}

	m, err = CompileAuto("G@os_family:Debian and web*", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match(web1()) || m.Match(db1()) {
		t.Error("a compound expression was not recognised")
	}
}

func TestAtSignThatIsNotATypeSigil(t *testing.T) {
	// A node ID or grain value containing @ must not be mistaken for a
	// type sigil.
	n := Node{ID: "svc@host.prod"}
	mustMatch(t, Compound, "svc@host.prod", n, true)
	mustMatch(t, Compound, "svc@*", n, true)
}

func TestKindFromFlag(t *testing.T) {
	for flag, want := range map[string]Kind{
		"": Glob, "L": List, "E": Regex, "G": Grain, "P": GrainRegex,
		"I": Pillar, "J": PillarRegex, "S": Subnet, "N": Nodegroup, "C": Compound,
	} {
		got, ok := KindFromFlag(flag)
		if !ok || got != want {
			t.Errorf("flag %q = %v (%v), want %v", flag, got, ok, want)
		}
	}
	// SECO range is the one target type Salt has that halite does not.
	if _, ok := KindFromFlag("R"); ok {
		t.Error("range targeting is not supported and must not resolve")
	}
}
