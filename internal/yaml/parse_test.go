package yaml

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

func parse(t *testing.T, src string) any {
	t.Helper()
	v, _, err := Parse([]byte(src), DefaultOptions("test.sls"))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	return v
}

func parseWarn(t *testing.T, src string) (any, []Warning) {
	t.Helper()
	v, w, err := Parse([]byte(src), DefaultOptions("test.sls"))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	return v, w
}

func mustFail(t *testing.T, src, wantSubstring string) *Error {
	t.Helper()
	_, _, err := Parse([]byte(src), DefaultOptions("test.sls"))
	if err == nil {
		t.Fatalf("expected an error containing %q, got none", wantSubstring)
	}
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("error %q does not contain %q", err.Error(), wantSubstring)
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", err)
	}
	return e
}

// get walks a colon path and fails the test if it does not resolve.
func get(t *testing.T, root any, path string) any {
	t.Helper()
	v, ok := value.Traverse(root, path, ":")
	if !ok {
		t.Fatalf("path %q did not resolve in %#v", path, root)
	}
	return v
}

func TestBlockMappingPreservesOrder(t *testing.T) {
	v := parse(t, "zebra: 1\napple: 2\nmango: 3\n")
	m, ok := v.(*value.Map)
	if !ok {
		t.Fatalf("expected a mapping, got %T", v)
	}
	want := []string{"zebra", "apple", "mango"}
	if got := m.StringKeys(); !equalStrings(got, want) {
		t.Fatalf("key order = %v, want %v (state run order depends on it)", got, want)
	}
}

func TestNestedBlockStructures(t *testing.T) {
	src := `
nginx_installed:
  pkg.installed:
    - name: nginx
    - version: 1.24.*

/etc/nginx/nginx.conf:
  file.managed:
    - source: salt://webserver/files/nginx.conf
    - mode: '0644'
    - require:
      - pkg: nginx_installed
`
	v := parse(t, src)
	if got := get(t, v, "nginx_installed:pkg.installed:0:name"); got != "nginx" {
		t.Errorf("name = %#v, want nginx", got)
	}
	if got := get(t, v, "/etc/nginx/nginx.conf:file.managed:1:mode"); got != "0644" {
		t.Errorf("mode = %#v, want the string 0644", got)
	}
	req := get(t, v, "/etc/nginx/nginx.conf:file.managed:2:require")
	items, ok := req.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("require = %#v, want a one-item sequence", req)
	}
	if got := get(t, items[0], "pkg"); got != "nginx_installed" {
		t.Errorf("requisite target = %#v", got)
	}
}

func TestSequenceAtSameIndentAsKey(t *testing.T) {
	// Salt trees are written both ways; both must parse the same.
	indented := parse(t, "include:\n  - a\n  - b\n")
	flush := parse(t, "include:\n- a\n- b\n")
	for _, v := range []any{indented, flush} {
		got := get(t, v, "include")
		items, ok := got.([]any)
		if !ok || len(items) != 2 || items[0] != "a" || items[1] != "b" {
			t.Fatalf("include = %#v, want [a b]", got)
		}
	}
}

func TestScalarResolution(t *testing.T) {
	cases := []struct {
		src  string
		want any
	}{
		{"v: true", true},
		{"v: True", true},
		{"v: TRUE", true},
		{"v: false", false},
		{"v: FALSE", false},
		{"v: null", nil},
		{"v: Null", nil},
		{"v: ~", nil},
		{"v:", nil},
		{"v: 42", int64(42)},
		{"v: -42", int64(-42)},
		{"v: +42", int64(42)},
		{"v: 0x1F", int64(31)},
		{"v: 0o17", "0o17"},
		{"v: 017", int64(15)},
		{"v: 1_000", int64(1000)},
		{"v: 1.5", 1.5},
		{"v: -2.5e3", "-2.5e3"},
		{"v: hello", "hello"},
		{"v: 2026-08-19", "2026-08-19"},
		{"v: 2026-08-19T14:22:11Z", "2026-08-19T14:22:11Z"},
		{"v: '0644'", "0644"},
		{"v: \"true\"", "true"},
		{"v: 1:30", "1:30"},
		{"v: 1.24.*", "1.24.*"},
		{"v: 3.10", 3.10},
		{"v: nginx-1.0", "nginx-1.0"},
		{"v: inf", "inf"},
		{"v: nan", "nan"},
	}
	for _, c := range cases {
		got := get(t, parse(t, c.src), "v")
		if got != c.want {
			t.Errorf("%s -> %#v (%T), want %#v (%T)", c.src, got, got, c.want, c.want)
		}
	}
}

func TestInfinityAndNaN(t *testing.T) {
	if got := get(t, parse(t, "v: .inf"), "v"); !math.IsInf(got.(float64), 1) {
		t.Errorf(".inf = %#v", got)
	}
	if got := get(t, parse(t, "v: -.Inf"), "v"); !math.IsInf(got.(float64), -1) {
		t.Errorf("-.Inf = %#v", got)
	}
	if got := get(t, parse(t, "v: .NAN"), "v"); !math.IsNaN(got.(float64)) {
		t.Errorf(".NAN = %#v", got)
	}
}

func TestYAML11Booleans(t *testing.T) {
	for _, s := range Bool11Spellings() {
		v, warns := parseWarn(t, "v: "+s+"\n")
		got := get(t, v, "v")
		if _, ok := got.(bool); !ok {
			t.Errorf("%q resolved to %#v (%T), want a bool under YAML 1.1", s, got, got)
		}
		if len(warns) != 1 || warns[0].Kind != WarnBool11 {
			t.Errorf("%q produced warnings %v, want exactly one WarnBool11", s, warns)
		}
	}
}

func TestYAML12ModeKeepsStrings(t *testing.T) {
	opts := DefaultOptions("test.sls")
	opts.Bool11 = false
	v, warns, err := Parse([]byte("v: yes\nw: on\n"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := get(t, v, "v"); got != "yes" {
		t.Errorf("with Bool11 off, yes = %#v, want the string", got)
	}
	if len(warns) != 0 {
		t.Errorf("Bool11 off should warn about nothing, got %v", warns)
	}
}

func TestOctalWarningNamesTheValue(t *testing.T) {
	_, warns := parseWarn(t, "mode: 0644\n")
	if len(warns) != 1 || warns[0].Kind != WarnOctalImplicit {
		t.Fatalf("warnings = %v, want one WarnOctalImplicit", warns)
	}
	if warns[0].Pos.Line != 1 {
		t.Errorf("warning position = %v, want line 1", warns[0].Pos)
	}
}

func TestQuotedScalars(t *testing.T) {
	cases := []struct{ src, want string }{
		{`v: 'it''s here'`, "it's here"},
		{`v: "tab\there"`, "tab\there"},
		{`v: "nl\nhere"`, "nl\nhere"},
		{`v: "\x41\u0042\U00000043"`, "ABC"},
		{`v: "quote\"inside"`, `quote"inside`},
		{`v: 'no \n escape'`, `no \n escape`},
		{`v: "back\\slash"`, `back\slash`},
		{`v: "\0"`, "\x00"},
		{`v: "\e"`, "\x1b"},
	}
	for _, c := range cases {
		if got := get(t, parse(t, c.src), "v"); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestFlowCollections(t *testing.T) {
	v := parse(t, "list: [1, 2, three]\nmap: {a: 1, b: [x, y]}\nnested: [{k: v}]\n")
	items := get(t, v, "list").([]any)
	if len(items) != 3 || items[0] != int64(1) || items[2] != "three" {
		t.Errorf("list = %#v", items)
	}
	if got := get(t, v, "map:a"); got != int64(1) {
		t.Errorf("map:a = %#v", got)
	}
	if got := get(t, v, "map:b:1"); got != "y" {
		t.Errorf("map:b:1 = %#v", got)
	}
	if got := get(t, v, "nested:0:k"); got != "v" {
		t.Errorf("nested:0:k = %#v", got)
	}
}

func TestFlowTrailingComma(t *testing.T) {
	v := parse(t, "list: [a, b,]\n")
	if items := get(t, v, "list").([]any); len(items) != 2 {
		t.Errorf("list = %#v, want 2 items", items)
	}
}

func TestLiteralBlockScalar(t *testing.T) {
	src := "contents: |\n  line one\n  line two\n"
	if got := get(t, parse(t, src), "contents"); got != "line one\nline two\n" {
		t.Errorf("literal = %q", got)
	}
}

func TestBlockScalarChomping(t *testing.T) {
	cases := []struct{ src, want string }{
		{"c: |\n  a\n  b\n", "a\nb\n"},
		{"c: |-\n  a\n  b\n", "a\nb"},
		{"c: |+\n  a\n  b\n\n\n", "a\nb\n\n\n"},
		{"c: >\n  a\n  b\n", "a b\n"},
		{"c: >-\n  a\n  b\n", "a b"},
	}
	for _, tc := range cases {
		if got := get(t, parse(t, tc.src), "c"); got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.src, got, tc.want)
		}
	}
}

func TestFoldedBlockKeepsMoreIndentedLines(t *testing.T) {
	// The rule naive implementations get wrong, and the one that matters
	// for a file.managed contents block holding indented configuration.
	src := "c: >\n  normal line\n  folded with it\n    more indented\n    stays broken\n  back to normal\n"
	want := "normal line folded with it\n  more indented\n  stays broken\nback to normal\n"
	if got := get(t, parse(t, src), "c"); got != want {
		t.Errorf("folded =\n%q\nwant\n%q", got, want)
	}
}

func TestBlockScalarIndentIndicator(t *testing.T) {
	src := "c: |2\n    indented by two\n  base\n"
	if got := get(t, parse(t, src), "c"); got != "  indented by two\nbase\n" {
		t.Errorf("indent indicator = %q", got)
	}
}

func TestBlockScalarBlankLines(t *testing.T) {
	src := "c: |\n  a\n\n  b\n"
	if got := get(t, parse(t, src), "c"); got != "a\n\nb\n" {
		t.Errorf("blank line handling = %q", got)
	}
}

func TestAnchorsAndAliases(t *testing.T) {
	src := "base: &b\n  a: 1\n  b: 2\nuse: *b\n"
	v := parse(t, src)
	if got := get(t, v, "use:a"); got != int64(1) {
		t.Errorf("alias did not carry the mapping: %#v", got)
	}
}

func TestMergeKeySingle(t *testing.T) {
	src := "defaults: &d\n  port: 80\n  tls: false\nsite:\n  <<: *d\n  tls: true\n"
	v := parse(t, src)
	if got := get(t, v, "site:port"); got != int64(80) {
		t.Errorf("merged key missing: %#v", got)
	}
	if got := get(t, v, "site:tls"); got != true {
		t.Errorf("explicit key must beat the merge, got %#v", got)
	}
}

func TestMergeKeySequencePrefersEarlier(t *testing.T) {
	src := "a: &a\n  k: from_a\nb: &b\n  k: from_b\n  only_b: 1\nc:\n  <<: [*a, *b]\n"
	v := parse(t, src)
	if got := get(t, v, "c:k"); got != "from_a" {
		t.Errorf("merge sequence precedence: k = %#v, want from_a", got)
	}
	if got := get(t, v, "c:only_b"); got != int64(1) {
		t.Errorf("only_b = %#v", got)
	}
}

func TestMergeKeyOrdering(t *testing.T) {
	src := "d: &d\n  mid: 1\ns:\n  first: 0\n  <<: *d\n  last: 2\n"
	m := get(t, parse(t, src), "s").(*value.Map)
	want := []string{"first", "mid", "last"}
	if got := m.StringKeys(); !equalStrings(got, want) {
		t.Errorf("merged key order = %v, want %v", got, want)
	}
}

func TestExplicitKey(t *testing.T) {
	v := parse(t, "? simple\n: value\n")
	if got := get(t, v, "simple"); got != "value" {
		t.Errorf("explicit key = %#v", got)
	}
}

func TestTags(t *testing.T) {
	if got := get(t, parse(t, "v: !!str 42"), "v"); got != "42" {
		t.Errorf("!!str = %#v", got)
	}
	if got := get(t, parse(t, "v: !!int 42"), "v"); got != int64(42) {
		t.Errorf("!!int = %#v", got)
	}
	if got := get(t, parse(t, "v: !!float 42"), "v"); got != 42.0 {
		t.Errorf("!!float = %#v", got)
	}
	if got := get(t, parse(t, "v: !!bool yes"), "v"); got != true {
		t.Errorf("!!bool = %#v", got)
	}
	if got := get(t, parse(t, "v: !!binary aGVsbG8="), "v"); string(got.([]byte)) != "hello" {
		t.Errorf("!!binary = %#v", got)
	}
	got := get(t, parse(t, "v: !!timestamp 2026-08-19T14:22:11Z"), "v")
	ts, ok := got.(time.Time)
	if !ok || ts.Year() != 2026 {
		t.Errorf("!!timestamp = %#v", got)
	}
}

func TestComments(t *testing.T) {
	src := "# leading\nkey: value # trailing\n# between\nother: 2\n"
	v := parse(t, src)
	if got := get(t, v, "key"); got != "value" {
		t.Errorf("key = %#v", got)
	}
	if got := get(t, v, "other"); got != int64(2) {
		t.Errorf("other = %#v", got)
	}
}

func TestHashNotAComment(t *testing.T) {
	if got := get(t, parse(t, "url: http://host/path#frag\n"), "url"); got != "http://host/path#frag" {
		t.Errorf("a # not preceded by whitespace is literal, got %#v", got)
	}
}

func TestMultilinePlainScalar(t *testing.T) {
	src := "comment: this is a long\n  comment that wraps\nother: 1\n"
	v := parse(t, src)
	if got := get(t, v, "comment"); got != "this is a long comment that wraps" {
		t.Errorf("folded plain scalar = %#v", got)
	}
	if got := get(t, v, "other"); got != int64(1) {
		t.Errorf("parsing did not resume after the folded scalar: %#v", got)
	}
}

func TestBOMIsConsumed(t *testing.T) {
	v := parse(t, "\uFEFFkey: value\n")
	if got := get(t, v, "key"); got != "value" {
		t.Errorf("BOM handling: %#v", got)
	}
}

// ---- rejections, SPEC section 10.1.2 ----

func TestRejectsPythonTags(t *testing.T) {
	mustFail(t, "v: !!python/object/apply:os.system ['id']\n", "unsupported tag")
}

func TestRejectsUnknownTag(t *testing.T) {
	mustFail(t, "v: !!set {a}\n", "unsupported tag")
}

func TestRejectsDuplicateKeysNamingBoth(t *testing.T) {
	e := mustFail(t, "a: 1\nb: 2\na: 3\n", "duplicate mapping key")
	if e.Pos.Line != 3 {
		t.Errorf("duplicate reported at line %d, want 3", e.Pos.Line)
	}
	if len(e.Related) != 1 || e.Related[0].Pos.Line != 1 {
		t.Errorf("error must name the first occurrence, got %+v", e.Related)
	}
}

func TestRejectsTabIndentation(t *testing.T) {
	mustFail(t, "a:\n\tb: 1\n", "tab character used for indentation")
}

func TestRejectsComplexKeys(t *testing.T) {
	mustFail(t, "? [a, b]\n: value\n", "cannot be used as a key")
	mustFail(t, "{[a]: b}\n", "cannot be used as a key")
}

func TestRejectsNonUTF8(t *testing.T) {
	_, _, err := Parse([]byte{'a', ':', ' ', 0xff, 0xfe}, DefaultOptions("test.sls"))
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("expected a UTF-8 error, got %v", err)
	}
}

func TestRejectsUndefinedAlias(t *testing.T) {
	mustFail(t, "v: *nope\n", "has not been defined")
}

func TestRejectsMultipleDocumentsInOneFile(t *testing.T) {
	mustFail(t, "a: 1\n---\nb: 2\n", "more than one YAML document")
}

func TestStreamAllowsMultipleDocuments(t *testing.T) {
	docs, _, err := ParseStream([]byte("a: 1\n---\nb: 2\n"), DefaultOptions("test.sls"))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d documents, want 2", len(docs))
	}
}

func TestTerminatorDoesNotMakeASecondDocument(t *testing.T) {
	v := parse(t, "a: 1\n...\n")
	if got := get(t, v, "a"); got != int64(1) {
		t.Errorf("a = %#v", got)
	}
}

func TestAliasBombIsBounded(t *testing.T) {
	// The billion-laughs shape: each level doubles the node count.
	var b strings.Builder
	b.WriteString("a: &a [x, x, x, x, x, x, x, x, x, x]\n")
	prev := "a"
	for i := 0; i < 8; i++ {
		name := string(rune('b' + i))
		b.WriteString(name + ": &" + name + " [")
		for j := 0; j < 10; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString("*" + prev)
		}
		b.WriteString("]\n")
		prev = name
	}
	opts := DefaultOptions("bomb.sls")
	_, _, err := Parse([]byte(b.String()), opts)
	if err == nil {
		t.Fatal("an alias expansion bomb must be refused")
	}
	if !strings.Contains(err.Error(), "node budget") {
		t.Fatalf("error should name the node budget, got %v", err)
	}
}

func TestDepthIsBounded(t *testing.T) {
	src := strings.Repeat("a: {", 200) + strings.Repeat("}", 200)
	_, _, err := Parse([]byte(src), DefaultOptions("deep.sls"))
	if err == nil || !strings.Contains(err.Error(), "nesting deeper") {
		t.Fatalf("expected a depth error, got %v", err)
	}
}

func TestErrorCarriesPositionAndLine(t *testing.T) {
	e := mustFail(t, "good: 1\nbad: [unterminated\n", "flow sequence")
	if e.Pos.File != "test.sls" {
		t.Errorf("error file = %q", e.Pos.File)
	}
	if e.Pos.Line != 2 {
		t.Errorf("error line = %d, want 2", e.Pos.Line)
	}
	if !strings.Contains(e.Detail(), "bad: [unterminated") {
		t.Errorf("Detail should show the source line, got:\n%s", e.Detail())
	}
}

func TestMappingEntriesCarryPositions(t *testing.T) {
	v := parse(t, "first: 1\nsecond: 2\n")
	m := v.(*value.Map)
	e, ok := m.Entry("second")
	if !ok {
		t.Fatal("second missing")
	}
	if e.KeyPos.Line != 2 || e.KeyPos.Col != 1 {
		t.Errorf("key position = %v, want line 2 column 1", e.KeyPos)
	}
	if e.KeyPos.File != "test.sls" {
		t.Errorf("key position file = %q", e.KeyPos.File)
	}
}

func TestEmptyDocument(t *testing.T) {
	if v := parse(t, ""); v != nil {
		t.Errorf("empty document = %#v, want nil", v)
	}
	if v := parse(t, "# only a comment\n"); v != nil {
		t.Errorf("comment-only document = %#v, want nil", v)
	}
}

func TestTopLevelSequence(t *testing.T) {
	v := parse(t, "- one\n- two\n")
	items, ok := v.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("top-level sequence = %#v", v)
	}
}

func TestSequenceOfMappings(t *testing.T) {
	src := "- name: a\n  value: 1\n- name: b\n  value: 2\n"
	items := parse(t, src).([]any)
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	if got := get(t, items[1], "name"); got != "b" {
		t.Errorf("second name = %#v", got)
	}
}

func TestNullSequenceItem(t *testing.T) {
	items := parse(t, "- a\n-\n- c\n").([]any)
	if len(items) != 3 || items[1] != nil {
		t.Errorf("items = %#v", items)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSingleLetterYNStayStrings records a deliberate divergence from SPEC
// section 10.1.3, whose resolution table lists y, Y, n, and N as YAML 1.1
// booleans.
//
// PyYAML does not resolve the single letters, so Salt does not either.
// Following the table as written would make `name: n` a boolean here and a
// string in Salt, breaking the compatibility the section exists to
// preserve. See docs/DIVERGENCE.md.
func TestSingleLetterYNStayStrings(t *testing.T) {
	for _, s := range []string{"y", "Y", "n", "N"} {
		v, warns := parseWarn(t, "v: "+s+"\n")
		got := get(t, v, "v")
		if got != s {
			t.Errorf("%q resolved to %#v (%T); PyYAML leaves the single letters as strings", s, got, got)
		}
		if len(warns) != 0 {
			t.Errorf("%q produced warnings %v; there is nothing ambiguous about it", s, warns)
		}
	}
	// The spellings PyYAML *does* resolve are unaffected.
	for _, s := range []string{"yes", "no", "on", "off", "Yes", "NO"} {
		got := get(t, parse(t, "v: "+s+"\n"), "v")
		if _, ok := got.(bool); !ok {
			t.Errorf("%q should still resolve to a bool, got %#v", s, got)
		}
	}
}
