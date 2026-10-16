package template

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"os"
	"os/exec"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"testing"
	"time"
)

// These tests hold the resource limits of DIVERGENCE 5.312. The engine
// runs in the hub's own process when it compiles pillar, and in the
// node's unless render_sandbox is on, so a template that can overflow the
// Go stack or ask for more memory than exists ends that process: both are
// fatal errors, which recover does not see. Every case here uses a small
// input against a small limit, or a child process with a small stack, so
// that proving the bound does not need the machine to run out of anything.

// smallEnv is an environment whose limits are small enough to cross with
// a few bytes of template.
func smallEnv() *Environment {
	o := DefaultOptions()
	o.MaxOutput = 1000
	o.MaxIterations = 1000
	return NewEnvironment(nil, o)
}

func renderSmall(src string) (string, error) {
	res, err := smallEnv().RenderString(src, "t.sls", nil)
	return res.Output, err
}

// ---- nesting ----

// Each shape builds an expression of the given height: the parser's
// recursion and the tree's height count the same thing for these, so
// shape(maxExprDepth) is the tallest that is allowed.
var exprShapes = map[string]func(n int) string{
	// The outermost expression is one level and each pair of
	// parentheses one more.
	"parens": func(n int) string { return strings.Repeat("(", n-1) + "1" + strings.Repeat(")", n-1) },
	"not":    func(n int) string { return strings.Repeat("not ", n-1) + "1" },
	"neg":    func(n int) string { return strings.Repeat("-", n-1) + "1" },
	// A chain of n terms is n-1 operators over a leaf, built by a loop
	// that never recurses, so only the height check catches it.
	"plus-chain": func(n int) string { return strings.TrimSuffix(strings.Repeat("1 + ", n), " + ") },
	"filter-chain": func(n int) string {
		return "1" + strings.Repeat("|abs", n-1)
	},
	"attr-chain": func(n int) string { return "x" + strings.Repeat(".a", n-1) },
	"list":       func(n int) string { return strings.Repeat("[", n-1) + "1" + strings.Repeat("]", n-1) },
	"dict": func(n int) string {
		return strings.Repeat("{'a': ", n-1) + "1" + strings.Repeat("}", n-1)
	},
	"cond-else": func(n int) string { return strings.Repeat("1 if 0 else ", n-1) + "2" },
	"pow":       func(n int) string { return strings.TrimSuffix(strings.Repeat("1 ** ", n), " ** ") },
}

func isNestingError(err error) bool {
	var te *Error
	return errors.As(err, &te) && strings.Contains(te.Msg, "nests deeper than") && te.Pos.Line > 0
}

// TestExpressionNestingLimitEdge holds the limit from both sides for every
// way an expression can nest: at the limit a template parses, and one
// level more is an ordinary error with a position.
func TestExpressionNestingLimitEdge(t *testing.T) {
	env := NewEnvironment(nil, DefaultOptions())
	for name, shape := range exprShapes {
		t.Run(name, func(t *testing.T) {
			if _, err := env.Parse("{{ "+shape(maxExprDepth)+" }}", "t.sls"); err != nil {
				t.Fatalf("at the limit (%d): %v", maxExprDepth, err)
			}
			_, err := env.Parse("{{ "+shape(maxExprDepth+1)+" }}", "t.sls")
			if !isNestingError(err) {
				t.Fatalf("one past the limit: want a positioned nesting error, got %v", err)
			}
		})
	}
}

// TestExpressionHeightCheckedInEveryStatement puts a chain one past the
// limit into each place a statement holds an expression. The chain is
// built by a loop, so only the walk over the finished tree sees it, and
// that walk has to reach every statement's expressions to see it there.
func TestExpressionHeightCheckedInEveryStatement(t *testing.T) {
	deep := exprShapes["plus-chain"](maxExprDepth + 1)
	filters := "x" + strings.Repeat("|abs", maxExprDepth)
	cases := map[string]string{
		"output":       "{{ " + deep + " }}",
		"if":           "{% if " + deep + " %}{% endif %}",
		"elif":         "{% if 1 %}{% elif " + deep + " %}{% endif %}",
		"for iter":     "{% for i in " + deep + " %}{% endfor %}",
		"for cond":     "{% for i in x if " + deep + " %}{% endfor %}",
		"set":          "{% set x = " + deep + " %}",
		"filter tag":   "{% filter " + strings.TrimPrefix(filters, "x|") + " %}{% endfilter %}",
		"macro param":  "{% macro m(a=" + deep + ") %}{% endmacro %}",
		"call":         "{% call m(" + deep + ") %}{% endcall %}",
		"call param":   "{% call(a=" + deep + ") m() %}{% endcall %}",
		"include":      "{% include " + deep + " %}",
		"import":       "{% import " + deep + " as m %}",
		"from import":  "{% from " + deep + " import m %}",
		"import_yaml":  "{% import_yaml " + deep + " as m %}",
		"extends":      "{% extends " + deep + " %}",
		"do":           "{% do " + deep + " %}",
		"with":         "{% with a = " + deep + " %}{% endwith %}",
		"nested in if": "{% if 1 %}{% for i in x %}{{ " + deep + " }}{% endfor %}{% endif %}",
		"kwarg":        "{{ f(a=" + deep + ") }}",
		"slice":        "{{ x[" + deep + ":] }}",
	}
	env := NewEnvironment(nil, DefaultOptions())
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := env.Parse(src, "t.sls"); !isNestingError(err) {
				t.Fatalf("want a positioned nesting error, got %v", err)
			}
		})
	}
}

// TestStatementNestingLimitEdge holds the statement bound, which is the
// render's own MaxDepth.
func TestStatementNestingLimitEdge(t *testing.T) {
	env := NewEnvironment(nil, DefaultOptions())
	limit := env.Opts.MaxDepth
	nested := func(n int) string {
		return strings.Repeat("{% if 1 %}", n) + "y" + strings.Repeat("{% endif %}", n)
	}
	if _, err := env.Parse(nested(limit), "t.sls"); err != nil {
		t.Fatalf("at the limit (%d): %v", limit, err)
	}
	_, err := env.Parse(nested(limit+1), "t.sls")
	var te *Error
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "statements nest deeper than") || te.Pos.Col == 0 {
		t.Fatalf("one past the limit: want a positioned nesting error, got %v", err)
	}
	// renderNode counts the text inside as a level too, so the deepest
	// statement nesting that renders is one less than the parse limit:
	// the parser never refuses what the renderer would have rendered.
	res, err := env.RenderString(nested(limit-1), "t.sls", nil)
	if err != nil || res.Output != "y" {
		t.Fatalf("%d nested ifs: %q, %v", limit-1, res.Output, err)
	}
	// Loops, macros and the rest nest through the same statement.
	mixed := strings.Repeat("{% for i in [1] %}{% with a = 1 %}", (limit+2)/2) + "y" +
		strings.Repeat("{% endwith %}{% endfor %}", (limit+2)/2)
	if _, err := env.Parse(mixed, "t.sls"); err == nil || !strings.Contains(err.Error(), "statements nest deeper") {
		t.Fatalf("mixed nesting past the limit: %v", err)
	}
}

// ---- the stack ----

const stackHelperEnv = "HALITE_TEMPLATE_STACK_HELPER"

// childStack is the stack the child runs with. It has two jobs that pull
// in opposite directions. It must be small enough that every hostile
// shape below overflowed it on the old code: each did at 4, 16 and 32
// MiB, and `elses` survived 64 MiB, so it is no larger than 32. And it
// must be large enough that the tallest legal template renders with
// room to spare on every platform CI runs: at-limit-ifs needed between 3
// and 4 MiB on darwin/arm64, with and without -race, and overflowed 4
// MiB under -race on windows-2022/amd64. The first version of this test
// used 4 MiB and failed there. 32 MiB is eight times what that run is
// known to have exceeded, and a 32nd of the 1 GiB a goroutine may grow
// to by default.
const childStack = 32 << 20

// hostileTemplates are templates the old code could not survive with a
// childStack stack. Each is under two megabytes of source. ok means the
// fixed code must parse and render it; otherwise it must refuse it with
// an ordinary error.
var hostileTemplates = map[string]struct {
	src func() string
	ok  bool
}{
	"parens":   {src: func() string { return "{{ " + exprShapes["parens"](300_000) + " }}" }},
	"nots":     {src: func() string { return "{{ " + exprShapes["not"](300_000) + " }}" }},
	"negs":     {src: func() string { return "{{ " + exprShapes["neg"](300_000) + " }}" }},
	"lists":    {src: func() string { return "{{ " + exprShapes["list"](200_000) + " }}" }},
	"dicts":    {src: func() string { return "{{ " + exprShapes["dict"](100_000) + " }}" }},
	"chain":    {src: func() string { return "{{ " + exprShapes["plus-chain"](300_000) + " }}" }},
	"filters":  {src: func() string { return "{{ " + exprShapes["filter-chain"](200_000) + " }}" }},
	"elses":    {src: func() string { return "{{ " + exprShapes["cond-else"](100_000) + " }}" }},
	"nestedif": {src: func() string { return strings.Repeat("{% if 1 %}", 100_000) + strings.Repeat("{% endif %}", 100_000) }},
	// The tallest templates the limits allow must still render on the
	// small stack, which is what makes the limits safe ones.
	"at-limit-chain":   {src: func() string { return "{{ " + exprShapes["plus-chain"](maxExprDepth) + " }}" }, ok: true},
	"at-limit-parens":  {src: func() string { return "{{ " + exprShapes["parens"](maxExprDepth) + " }}" }, ok: true},
	"at-limit-filters": {src: func() string { return "{{ " + exprShapes["filter-chain"](maxExprDepth) + " }}" }, ok: true},
	"at-limit-ifs": {src: func() string {
		return strings.Repeat("{% if 1 %}", 99) + "{{ " + exprShapes["parens"](maxExprDepth) + " }}" + strings.Repeat("{% endif %}", 99)
	}, ok: true},
}

// TestTemplateStackHelper is the child. It does nothing in a normal run.
func TestTemplateStackHelper(t *testing.T) {
	name := os.Getenv(stackHelperEnv)
	if name == "" {
		t.Skip("child process for TestHostileTemplatesDoNotOverflowTheStack; runs only when that test starts it")
	}
	h, ok := hostileTemplates[name]
	if !ok {
		t.Fatalf("no template %q", name)
	}
	debug.SetMaxStack(childStack)
	src := h.src()
	env := NewEnvironment(nil, DefaultOptions())
	res, err := env.RenderString(src, "t.sls", nil)
	if h.ok {
		if err != nil {
			t.Fatalf("want a render, got %v", err)
		}
		fmt.Printf("SURVIVED: rendered %d bytes\n", len(res.Output))
		return
	}
	var te *Error
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "nest") {
		t.Fatalf("want a nesting error, got %v", err)
	}
	fmt.Println("SURVIVED: refused:", te.Msg)
}

func TestHostileTemplatesDoNotOverflowTheStack(t *testing.T) {
	if os.Getenv(stackHelperEnv) != "" {
		t.Skip("in the child")
	}
	names := make([]string, 0, len(hostileTemplates))
	for n := range hostileTemplates {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTemplateStackHelper$", "-test.count=1", "-test.v")
			cmd.Env = append(os.Environ(), stackHelperEnv+"="+name)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "SURVIVED") {
				// A stack overflow prints the whole goroutine trace,
				// which is mostly the same frame; keep the head of it.
				s := string(out)
				if i := strings.Index(s, "fatal error"); i >= 0 {
					s = s[i:min(len(s), i+600)]
				} else if len(s) > 2000 {
					s = s[len(s)-2000:]
				}
				t.Fatalf("template %q did not survive (%v):\n%s", name, err, s)
			}
		})
	}
}

// ---- parse time ----

// TestLexingIsLinear holds the lexer to linear time. It used to search
// the rest of the source for every opening delimiter at every tag, so a
// template without `{{` or `{#` in it, which is most templates of nothing
// but statements, cost the square of its length: 4,000 sequential `{% if
// %}` took 160 ms and 100,000 did not finish. A wall-clock ceiling would
// be flaky on a loaded runner, so this compares two sizes instead: four
// times the input may cost four times the time, with room for noise, and
// not sixteen.
func TestLexingIsLinear(t *testing.T) {
	timeLex := func(n int) time.Duration {
		src := strings.Repeat("{% if 1 %}x{% endif %}\n", n)
		best := time.Duration(1 << 62)
		for range 3 {
			start := time.Now()
			if _, err := lex(src, "t.sls", DefaultDelimiters(), lexOptions{}); err != nil {
				t.Fatal(err)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	small, large := timeLex(5_000), timeLex(20_000)
	if ratio := float64(large) / float64(small); ratio > 9 {
		t.Fatalf("lexing 4x the input took %.1fx the time (%v, %v); quadratic would be 16x", ratio, small, large)
	}
}

// TestLexerFindsDelimitersAfterOneIsConsumed covers the case the cache
// has to get right: a `{{` inside a string inside a tag is consumed by
// that tag, so the cached position falls behind the cursor and has to be
// searched for again rather than trusted or forgotten.
func TestLexerFindsDelimitersAfterOneIsConsumed(t *testing.T) {
	src := `{% set a = "{{" %}{# c #}{{ a }}-{% if 1 %}{{ "{#" }}{% endif %}{# d #}{{ 2 }}`
	got := render(t, src, nil)
	if got != "{{-{#2" {
		t.Fatalf("got %q", got)
	}
}

// ---- allocations ----

func TestAllocatingBuiltinsAreBounded(t *testing.T) {
	// Each of these asked for more than the small environment's 1000
	// bytes or 1000 items and, in the old code, built it anyway, panicked,
	// or looped without end. Each ends in `length` so that the output
	// limit, which the old code did apply to what was written, cannot be
	// what refuses it.
	refused := map[string]string{
		"indent width":           `{{ 'a\nb' | indent(width=2000) | length }}`,
		"indent pad times lines": `{{ ('\n' * 600) | indent(width='xx', blank=true) | length }}`,
		"center":                 `{{ 'a' | center(2000) | length }}`,
		"zfill":                  `{{ 'a'.zfill(2000) | length }}`,
		"list repetition":        `{{ ([1, 2] * 501) | length }}`,
		"list repetition huge":   `{{ ([1] * 10000000000) | length }}`,
		// 2 * 2^62 wraps to a negative product, which passed the old
		// multiplication check and reached strings.Repeat.
		"string repetition wraps": `{{ ('ab' * 4611686018427387904) | length }}`,
		"string repetition":       `{{ ('ab' * 501) | length }}`,
		"range spans int64":       `{{ range(-9223372036854775807, 9223372036854775807) | length }}`,
		"range too long":          `{{ range(1001) | length }}`,
		"batch fill":              `{{ [1] | batch(10000000000, 0) | list }}`,
		"slice count":             `{{ [1] | slice(10000000000) | list }}`,
		// 720 results is under the old count limit; 4,320 items is not.
		"permutations items": `{{ [1, 2, 3, 4, 5, 6] | permutations(6) | length }}`,
	}
	for name, src := range refused {
		t.Run(name, func(t *testing.T) {
			out, err := renderSmall(src)
			var te *Error
			if !errors.As(err, &te) || te.Pos.Line == 0 {
				t.Fatalf("want a positioned error, got %q, %v", out, err)
			}
			if strings.Contains(te.Msg, "internal error") {
				t.Fatalf("refused only by the panic backstop, not at the source: %v", err)
			}
		})
	}

	// At or under the limits, and with the arguments Python defines
	// without error, each gives what Jinja gives. The expectations were
	// taken from Jinja 3.1.6 on CPython 3.14.
	allowed := map[string][2]string{
		"indent negative":         {`{{ 'a\nb' | indent(width=-1) }}`, "a\nb"},
		"indent wide, one line":   {`{{ 'a' | indent(width=10000000000) }}`, "a"},
		"indent at limit":         {`{{ 'a\nb' | indent(width=997) | length }}`, "1000"},
		"center negative":         {`{{ 'abc' | center(-5) }}`, "abc"},
		"center":                  {`{{ 'abc' | center(7) }}`, "  abc  "},
		"center at limit":         {`{{ 'a' | center(1000) | length }}`, "1000"},
		"zfill negative":          {`{{ 'abc'.zfill(-3) }}`, "abc"},
		"zfill at limit":          {`{{ 'a'.zfill(1000) | length }}`, "1000"},
		"list repetition":         {`{{ ([1, 2] * 500) | length }}`, "1000"},
		"list repetition neg":     {`{{ [1] * -1 }}`, "[]"},
		"empty list repetition":   {`{{ [] * 9223372036854775807 }}`, "[]"},
		"string repetition":       {`{{ ('ab' * 500) | length }}`, "1000"},
		"string repetition neg":   {`{{ 'ab' * -1 }}`, ""},
		"range near int64 max":    {`{{ range(9223372036854775800, 9223372036854775807, 5) | list }}`, "[9223372036854775800, 9223372036854775805]"},
		"range down":              {`{{ range(5, 0, -2) | list }}`, "[5, 3, 1]"},
		"range at limit":          {`{{ range(1000) | length }}`, "1000"},
		"batch huge without fill": {`{{ [1] | batch(9223372036854775807) | list }}`, "[[1]]"},
		"batch":                   {`{{ [1, 2, 3] | batch(2, 0) | list }}`, "[[1, 2], [3, 0]]"},
		"slice":                   {`{{ [1, 2, 3] | slice(5) | list }}`, "[[1], [2], [3], [], []]"},
		"permutations":            {`{{ [1, 2, 3, 4, 5] | permutations(5) | length }}`, "120"},
		// Twelve is past Jinja's leeway of five over the length, which
		// halite does not implement; this case is the same either way.
		"truncate": {`{{ 'abcdefghijkl' | truncate(3, true) }}`, "..."},
	}
	for name, c := range allowed {
		t.Run(name, func(t *testing.T) {
			out, err := renderSmall(c[0])
			if err != nil || out != c[1] {
				t.Fatalf("%s = %q, %v; want %q", c[0], out, err, c[1])
			}
		})
	}
}

// TestPanicsAtTheirSource covers the inputs that used to panic out of
// RenderString. Each must now be an ordinary error raised where the bad
// argument is, not the boundary's recovered panic.
func TestPanicsAtTheirSource(t *testing.T) {
	cases := map[string]string{
		"indent negative was a panic": `{{ 'a\nb' | indent(width=-1) }}x`,
		"zfill without an argument":   `{{ 'abc'.zfill() }}`,
		"truncate negative":           `{{ 'abcdef' | truncate(-9223372036854775807) }}`,
		"truncate below end":          `{{ 'abcdef' | truncate(2) }}`,
		"string repetition wraps":     `{{ 'ab' * 4611686018427387904 }}`,
		"cidr_subnets count wraps":    `{{ '::/0' | cidr_subnets(63) }}`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := renderSmall(src)
			if err != nil && strings.Contains(err.Error(), "internal error") {
				t.Fatalf("reached the panic backstop: %v", err)
			}
		})
	}
	// The two Jinja refuses are errors here too.
	for _, src := range []string{cases["zfill without an argument"], cases["truncate below end"], cases["truncate negative"]} {
		if _, err := renderSmall(src); err == nil {
			t.Errorf("%s: want an error", src)
		}
	}
}

// TestCIDRSubnetsBeyondSixtyFourBits checks the IPv6 splits whose step
// does not fit an int64 against Python's ipaddress, which is what Salt's
// network helpers stand on.
func TestCIDRSubnetsBeyondSixtyFourBits(t *testing.T) {
	cases := map[string]string{
		// Was the same subnet twice: the step 1<<63 is negative.
		`{{ '2001:db8::/64' | cidr_subnets(65) }}`: "['2001:db8::/65', '2001:db8:0:0:8000::/65']",
		// Was 2^47 calls to Next per subnet.
		`{{ '2001:db8::/80' | cidr_subnets(81) }}`:    "['2001:db8::/81', '2001:db8::8000:0:0/81']",
		`{{ '10.0.0.0/24' | cidr_subnets(26) }}`:      "['10.0.0.0/26', '10.0.0.64/26', '10.0.0.128/26', '10.0.0.192/26']",
		`{{ '255.255.255.0/24' | cidr_subnets(25) }}`: "['255.255.255.0/25', '255.255.255.128/25']",
		`{{ '::/0' | cidr_subnets(1) }}`:              "['::/1', '8000::/1']",
	}
	for src, want := range cases {
		if got := render(t, src, nil); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
	if err := renderErr(t, `{{ '::/0' | cidr_subnets(17) }}`, nil); err == nil || !strings.Contains(err.Error(), "more than this filter will enumerate") {
		t.Errorf("2^17 subnets: %v", err)
	}
}

// TestPanicIsRecoveredAtTheBoundary is the backstop: a filter that panics
// costs the render, not the process.
func TestPanicIsRecoveredAtTheBoundary(t *testing.T) {
	env := NewEnvironment(nil, DefaultOptions())
	env.AddFilter("boom", func(*FilterContext, any, []any, map[string]any) (any, error) {
		var m map[string]int
		m["x"] = 1 // assignment to a nil map
		return nil, nil
	})
	_, err := env.RenderString("{{ 1 | boom }}", "t.sls", nil)
	if err == nil || !strings.Contains(err.Error(), "internal error rendering the template") ||
		!strings.Contains(err.Error(), "t.sls") {
		t.Fatalf("want a recovered error naming the template, got %v", err)
	}
	tpl, err := env.Parse("{{ 1 | boom }}", "u.sls")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.Render(nil); err == nil || !strings.Contains(err.Error(), "internal error") {
		t.Fatalf("Render: want a recovered error, got %v", err)
	}
}

// ---- the walker against ast.go ----

// TestDepthWalkCoversEveryField reads ast.go and checks that exprChildren
// and nodeParts visit every field that holds an expression or a body, for
// every node type there is. A field they miss is a subtree whose height
// nobody measures, which is the defect this whole check exists for.
func TestDepthWalkCoversEveryField(t *testing.T) {
	fset := gotoken.NewFileSet()
	f, err := goparser.ParseFile(fset, "ast.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range g.Specs {
			ts, ok := s.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, fl := range st.Fields.List {
				if id, ok := fl.Type.(*ast.Ident); ok && id.Name == "baseNode" && len(fl.Names) == 0 {
					declared = append(declared, ts.Name.Name)
				}
			}
		}
	}
	instances := map[string]any{}
	for _, v := range []any{
		&TextNode{}, &OutputNode{}, &IfNode{}, &ForNode{}, &SetNode{}, &MacroNode{},
		&CallNode{}, &IncludeNode{}, &ImportNode{}, &DataImportNode{}, &FromImportNode{},
		&ExtendsNode{}, &BlockNode{}, &FilterNode{}, &DoNode{}, &BreakNode{},
		&ContinueNode{}, &WithNode{}, &AutoescapeNode{},
		&LiteralExpr{}, &NameExpr{}, &AttrExpr{}, &ItemExpr{}, &SliceExpr{}, &CallExpr{},
		&FilterExpr{}, &TestExpr{}, &BinaryExpr{}, &UnaryExpr{}, &CondExpr{}, &ListExpr{},
		&TupleExpr{}, &DictExpr{},
	} {
		instances[reflect.TypeOf(v).Elem().Name()] = v
	}
	sort.Strings(declared)
	for _, name := range declared {
		if _, ok := instances[name]; !ok {
			t.Errorf("ast.go declares %s, which this test does not know: add it here and to exprChildren or nodeParts", name)
		}
	}
	if len(declared) != len(instances) {
		t.Errorf("ast.go declares %d node types, this test knows %d", len(declared), len(instances))
	}

	exprType := reflect.TypeOf((*Expr)(nil)).Elem()
	nodeType := reflect.TypeOf((*Node)(nil)).Elem()
	for name, inst := range instances {
		// Fill every field that can hold a subtree with a distinct
		// marker, then ask the walker for what it sees.
		var wantExprs []Expr
		var wantBodies [][]Node
		marker := func() Expr {
			e := &LiteralExpr{baseNode{Pos{Line: len(wantExprs) + 1}}, nil}
			wantExprs = append(wantExprs, e)
			return e
		}
		body := func() []Node {
			b := []Node{&TextNode{baseNode{Pos{Line: len(wantBodies) + 1}}, ""}}
			wantBodies = append(wantBodies, b)
			return b
		}
		v := reflect.ValueOf(inst).Elem()
		for i := 0; i < v.NumField(); i++ {
			fv, ft := v.Field(i), v.Type().Field(i)
			switch {
			case ft.Type == exprType:
				fv.Set(reflect.ValueOf(marker()))
			case ft.Type == reflect.SliceOf(exprType):
				fv.Set(reflect.ValueOf([]Expr{marker(), marker()}))
			case ft.Type == reflect.TypeOf([]KwArg{}):
				fv.Set(reflect.ValueOf([]KwArg{{"a", marker()}}))
			case ft.Type == reflect.TypeOf([]Param{}):
				fv.Set(reflect.ValueOf([]Param{{"a", marker()}}))
			case ft.Type == reflect.SliceOf(nodeType):
				fv.Set(reflect.ValueOf(body()))
			case ft.Type == reflect.SliceOf(reflect.SliceOf(nodeType)):
				fv.Set(reflect.ValueOf([][]Node{body(), body()}))
			case canHoldSubtree(ft.Type, exprType, nodeType):
				// A field of a shape this test does not fill is one
				// it cannot vouch for.
				t.Errorf("%s.%s is a %s, which this test does not know how to fill", name, ft.Name, ft.Type)
			}
		}
		var gotExprs []Expr
		var gotBodies [][]Node
		if e, ok := inst.(Expr); ok && strings.HasSuffix(name, "Expr") {
			gotExprs = exprChildren(e)
		} else {
			gotExprs, gotBodies = nodeParts(inst.(Node))
		}
		if !sameExprs(gotExprs, wantExprs) {
			t.Errorf("%s: the walk sees %d of its %d expression fields", name, countNonNil(gotExprs), len(wantExprs))
		}
		if !sameBodies(gotBodies, wantBodies) {
			t.Errorf("%s: the walk sees %d of its %d bodies", name, len(nonEmpty(gotBodies)), len(wantBodies))
		}
	}
}

// canHoldSubtree reports whether a value of type ty can reach an Expr or
// a Node. LiteralExpr.Val is an `any` holding a value, not a subtree, so
// an empty interface does not count.
func canHoldSubtree(ty, exprType, nodeType reflect.Type) bool {
	switch ty.Kind() {
	case reflect.Interface:
		return ty == exprType || ty == nodeType
	case reflect.Slice, reflect.Array, reflect.Pointer:
		return canHoldSubtree(ty.Elem(), exprType, nodeType)
	case reflect.Map:
		return canHoldSubtree(ty.Key(), exprType, nodeType) || canHoldSubtree(ty.Elem(), exprType, nodeType)
	case reflect.Struct:
		for i := 0; i < ty.NumField(); i++ {
			if canHoldSubtree(ty.Field(i).Type, exprType, nodeType) {
				return true
			}
		}
	}
	return false
}

func countNonNil(es []Expr) int {
	n := 0
	for _, e := range es {
		if e != nil {
			n++
		}
	}
	return n
}

func sameExprs(got, want []Expr) bool {
	seen := map[Expr]bool{}
	for _, e := range got {
		if e != nil {
			seen[e] = true
		}
	}
	if len(seen) != len(want) {
		return false
	}
	for _, e := range want {
		if !seen[e] {
			return false
		}
	}
	return true
}

func nonEmpty(bs [][]Node) [][]Node {
	var out [][]Node
	for _, b := range bs {
		if len(b) > 0 {
			out = append(out, b)
		}
	}
	return out
}

func sameBodies(got, want [][]Node) bool {
	got = nonEmpty(got)
	if len(got) != len(want) {
		return false
	}
	seen := map[Node]bool{}
	for _, b := range got {
		seen[b[0]] = true
	}
	for _, b := range want {
		if !seen[b[0]] {
			return false
		}
	}
	return true
}
