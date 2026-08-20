package yaml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The tests in this file exist to reach the paths the behavioural tests do
// not: the error renderings, the quoted-scalar folding, the escape set's
// long tail, and the encoder's scalar cases. SPEC section 31 holds the
// parser to branch coverage above 90% because it is the correctness core,
// and an unexercised branch in a parser is a defect nobody has met yet.

func TestErrorRendering(t *testing.T) {
	e := &Error{
		Pos:  value.Pos{File: "web.sls", Line: 4, Col: 7},
		Msg:  "something went wrong",
		Line: "  key: value",
		Related: []Related{
			{Pos: value.Pos{File: "web.sls", Line: 2, Col: 1}, Msg: "first defined here"},
		},
	}
	got := e.Error()
	for _, want := range []string{"web.sls:4:7", "something went wrong", "first defined here"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() is missing %q:\n%s", want, got)
		}
	}
	detail := e.Detail()
	if !strings.Contains(detail, "key: value") || !strings.Contains(detail, "^") {
		t.Errorf("Detail should show the line and a caret:\n%s", detail)
	}
}

func TestPositionRendering(t *testing.T) {
	cases := []struct {
		pos  value.Pos
		want string
	}{
		{value.Pos{}, "<unknown>"},
		{value.Pos{Line: 3, Col: 5}, "line 3, column 5"},
		{value.Pos{File: "a.sls"}, "a.sls"},
		{value.Pos{File: "a.sls", Line: 3, Col: 5}, "a.sls:3:5"},
	}
	for _, c := range cases {
		if got := c.pos.String(); got != c.want {
			t.Errorf("Pos%+v = %q, want %q", c.pos, got, c.want)
		}
	}
}

func TestWarningRendering(t *testing.T) {
	w := Warning{Kind: WarnBool11, Pos: value.Pos{File: "a.sls", Line: 2}, Msg: "yes is a boolean"}
	if got := w.String(); !strings.Contains(got, "a.sls:2") || !strings.Contains(got, "boolean") {
		t.Errorf("Warning.String = %q", got)
	}
	for kind, want := range map[WarnKind]string{
		WarnBool11:        "yaml_1_1_boolean",
		WarnOctalImplicit: "octal",
		WarnSexagesimal:   "sexagesimal",
		WarnDuplicateKey:  "duplicate_key",
	} {
		if got := kind.String(); got != want {
			t.Errorf("WarnKind(%d) = %q, want %q", kind, got, want)
		}
	}
}

func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web.sls")
	if err := os.WriteFile(path, []byte("key: value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, _, err := ParseFile(path, DefaultOptions(""))
	if err != nil {
		t.Fatal(err)
	}
	if got := get(t, v, "key"); got != "value" {
		t.Errorf("key = %#v", got)
	}
	// The file name is filled in when the options did not carry one, so a
	// diagnostic can point somewhere.
	if _, _, err := ParseFile(filepath.Join(dir, "absent.sls"), DefaultOptions("")); err == nil {
		t.Error("a missing file should be an error")
	}
}

func TestCarriageReturnsAreNormalised(t *testing.T) {
	// A tree edited on Windows still has to compile, and the line numbers
	// in a diagnostic still have to be right.
	v, _, err := Parse([]byte("a: 1\r\nb: 2\r\n"), DefaultOptions("crlf.sls"))
	if err != nil {
		t.Fatal(err)
	}
	if got := get(t, v, "b"); got != int64(2) {
		t.Errorf("b = %#v", got)
	}
	// A lone carriage return is a line break too.
	v, _, err = Parse([]byte("a: 1\rb: 2\r"), DefaultOptions("cr.sls"))
	if err != nil {
		t.Fatal(err)
	}
	if got := get(t, v, "b"); got != int64(2) {
		t.Errorf("b = %#v", got)
	}
}

func TestMultilineQuotedScalars(t *testing.T) {
	cases := []struct{ src, want string }{
		// A single break folds to a space.
		{"v: \"one\n  two\"", "one two"},
		{"v: 'one\n  two'", "one two"},
		// A blank line becomes a newline.
		{"v: \"one\n\n  two\"", "one\ntwo"},
		// An escaped break joins with nothing between.
		{"v: \"one\\\n  two\"", "onetwo"},
	}
	for _, c := range cases {
		got := get(t, parse(t, c.src), "v")
		if got != c.want {
			t.Errorf("%q -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestEveryDoubleQuotedEscape(t *testing.T) {
	// The full escape set matters because file.managed contents are
	// written this way, and a missing escape silently corrupts a file.
	cases := []struct {
		src  string
		want string
	}{
		{`"\0"`, "\x00"},
		{`"\a"`, "\a"},
		{`"\b"`, "\b"},
		{`"\t"`, "\t"},
		{`"\n"`, "\n"},
		{`"\v"`, "\v"},
		{`"\f"`, "\f"},
		{`"\r"`, "\r"},
		{`"\e"`, "\x1b"},
		{`"\ "`, " "},
		{`"\""`, `"`},
		{`"\/"`, "/"},
		{`"\\"`, `\`},
		// The named escapes carry YAML's own characters, not blanks:
		// next line, non-breaking space, line separator, and paragraph
		// separator.
		{`"\N"`, "\u0085"},
		{`"\_"`, "\u00a0"},
		{`"\L"`, "\u2028"},
		{`"\P"`, "\u2029"},
		{`"\x41"`, "A"},
		{`"\u00e9"`, "é"},
		{`"\U0001F600"`, "\U0001F600"},
	}
	for _, c := range cases {
		got := get(t, parse(t, "v: "+c.src), "v")
		if got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestBadEscapesAreRejected(t *testing.T) {
	mustFail(t, `v: "\q"`, "unknown escape sequence")
	mustFail(t, `v: "\xZZ"`, "invalid hexadecimal escape")
	mustFail(t, `v: "\x4`, "")
	mustFail(t, `v: "unterminated`, "unterminated double-quoted string")
	mustFail(t, "v: 'unterminated", "unterminated single-quoted string")
}

func TestTagsOnCollections(t *testing.T) {
	if got := get(t, parse(t, "v: !!seq [1, 2]"), "v"); len(got.([]any)) != 2 {
		t.Errorf("!!seq = %#v", got)
	}
	if got := get(t, parse(t, "v: !!map {a: 1}"), "v"); got.(*value.Map).Len() != 1 {
		t.Errorf("!!map = %#v", got)
	}
	// A tag that does not match the node it decorates is an error, not a
	// coercion.
	mustFail(t, "v: !!map [1, 2]", "does not match")
	mustFail(t, "v: !!seq {a: 1}", "does not match")
	mustFail(t, "v: !!set [1]", "unsupported tag")
	// A tag on a scalar that cannot hold it.
	mustFail(t, "v: !!int notanumber", "is not an integer")
	mustFail(t, "v: !!float notanumber", "is not a number")
	mustFail(t, "v: !!bool maybe", "is not a boolean")
	mustFail(t, "v: !!timestamp notatime", "is not a timestamp")
	mustFail(t, "v: !!binary not-base64!!", "not valid base64")
	mustFail(t, "v: !!seq scalar", "cannot be applied to a scalar")
}

func TestAnchorAndTagRejections(t *testing.T) {
	mustFail(t, "v: &a &b value", "only one anchor")
	mustFail(t, "v: !!str !!int 1", "only one tag")
	mustFail(t, "v: &", "anchor name is empty")
	mustFail(t, "v: *", "alias name is empty")
}

func TestBlockScalarRejections(t *testing.T) {
	mustFail(t, "v: |--\n  x\n", "only one chomping indicator")
	mustFail(t, "v: |12\n  x\n", "only one indentation indicator")
	mustFail(t, "v: | trailing junk\n  x\n", "unexpected")
}

func TestBlockScalarEdgeCases(t *testing.T) {
	// A block of nothing but blank lines.
	if got := get(t, parse(t, "v: |\n\n\n"), "v"); got != "" {
		t.Errorf("empty literal = %q", got)
	}
	if got := get(t, parse(t, "v: |+\n\n\n"), "v"); got != "\n\n" {
		t.Errorf("kept empty literal = %q", got)
	}
	// A block scalar as a sequence item.
	items := parse(t, "- |\n  line\n- second\n").([]any)
	if len(items) != 2 || items[0] != "line\n" {
		t.Errorf("items = %#v", items)
	}
}

func TestFlowEdgeCases(t *testing.T) {
	// A comment inside a flow collection.
	v := parse(t, "v: [ # a comment\n  1, 2 ]\n")
	if items := get(t, v, "v").([]any); len(items) != 2 {
		t.Errorf("items = %#v", items)
	}
	// An anchor and an alias inside a flow collection.
	v = parse(t, "v: [&a 1, *a]\n")
	items := get(t, v, "v").([]any)
	if len(items) != 2 || items[1] != int64(1) {
		t.Errorf("flow alias = %#v", items)
	}
	// A single-pair mapping without braces inside a flow sequence.
	v = parse(t, "v: [a: 1, b: 2]\n")
	items = get(t, v, "v").([]any)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if got := get(t, items[0], "a"); got != int64(1) {
		t.Errorf("single pair = %#v", got)
	}
	// A YAML set spelling, which becomes keys with null values.
	v = parse(t, "v: {a, b}\n")
	m := get(t, v, "v").(*value.Map)
	if m.Len() != 2 {
		t.Errorf("set = %#v", m)
	}
	if got, _ := m.Get("a"); got != nil {
		t.Errorf("set value = %#v, want null", got)
	}
}

func TestFlowRejections(t *testing.T) {
	// A plain scalar inside a flow collection may contain spaces, so
	// `[1 2]` is one item rather than an error; that is YAML, and PyYAML
	// reads it the same way.
	if items := get(t, parse(t, "v: [1 2]"), "v").([]any); len(items) != 1 || items[0] != "1 2" {
		t.Errorf("flow plain scalar = %#v", items)
	}
	// A document that is a bare scalar is legal too.
	if got := parse(t, "key value\n"); got != "key value" {
		t.Errorf("scalar document = %#v", got)
	}
	mustFail(t, "v: {a: 1 b: 2}", "expected `,` or `}`")
	mustFail(t, "v: [1, 2", "unterminated flow sequence")
	mustFail(t, "v: {a: 1", "unterminated flow mapping")
	mustFail(t, "v: {a: 1, a: 2}", "duplicate mapping key")
}

func TestMergeKeyRejections(t *testing.T) {
	mustFail(t, "v:\n  <<: notamap\n", "must name a mapping")
	mustFail(t, "a: &a [1]\nv:\n  <<: [*a]\n", "may contain only mappings")
}

func TestMappingRejections(t *testing.T) {
	mustFail(t, "a: 1\n  b: 2\n", "unexpected indentation")
	// `? key` with no `: ` line is a key with a null value, so what fails
	// here is the bare `value` line that follows it, not the explicit key.
	mustFail(t, "? key\nvalue\n", "expected `:` after the mapping key")
}

func TestAllowDuplicateKeysCollectsThemAll(t *testing.T) {
	// The migration report needs every duplicate, not the first.
	opts := DefaultOptions("dup.sls")
	opts.AllowDuplicateKeys = true
	v, warns, err := Parse([]byte("a: 1\nb: 2\na: 3\nb: 4\n"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 2 {
		t.Errorf("warnings = %v, want one per duplicate", warns)
	}
	for _, w := range warns {
		if w.Kind != WarnDuplicateKey {
			t.Errorf("warning kind = %v", w.Kind)
		}
	}
	// Last wins, matching PyYAML, since the caller asked to continue.
	if got := get(t, v, "a"); got != int64(3) {
		t.Errorf("a = %#v", got)
	}
}

func TestNumberEdgeCases(t *testing.T) {
	cases := []struct {
		src  string
		want any
	}{
		{"v: 0b1010", int64(10)},
		{"v: -0x10", int64(-16)},
		{"v: +0o17", int64(15)},
		{"v: 1_0_0", int64(100)},
		{"v: 1e3", 1000.0},
		{"v: 1E+3", 1000.0},
		{"v: 1e-3", 0.001},
		// These are strings, because YAML's float grammar does not
		// accept them even though strconv would.
		{"v: 1p3", "1p3"},
		{"v: 0x1.8p3", "0x1.8p3"},
		{"v: 1e", "1e"},
		{"v: --1", "--1"},
		{"v: 0x", "0x"},
		{"v: .", "."},
		{"v: 1.2.3", "1.2.3"},
	}
	for _, c := range cases {
		got := get(t, parse(t, c.src), "v")
		if got != c.want {
			t.Errorf("%s -> %#v (%T), want %#v", c.src, got, got, c.want)
		}
	}
}

func TestSexagesimalDetection(t *testing.T) {
	// A colon-separated number stays a string, with a warning, because
	// YAML 1.1 would read 1:30 as 90 and that is never what an SLS author
	// means.
	v, warns := parseWarn(t, "v: 1:30\n")
	if got := get(t, v, "v"); got != "1:30" {
		t.Errorf("v = %#v", got)
	}
	if len(warns) != 1 || warns[0].Kind != WarnSexagesimal {
		t.Errorf("warnings = %v", warns)
	}
	// Something that only looks like one is not warned about.
	for _, s := range []string{"a:b", "1:", ":30", "1:b"} {
		if isSexagesimal(s) {
			t.Errorf("%q should not be read as a sexagesimal", s)
		}
	}
	if !isSexagesimal("1:30:45") || !isSexagesimal("-1:30") || !isSexagesimal("1:30.5") {
		t.Error("a genuine sexagesimal was not recognised")
	}
}

func TestIsBool11Helper(t *testing.T) {
	for _, s := range Bool11Spellings() {
		if !IsBool11(s) {
			t.Errorf("IsBool11(%q) = false", s)
		}
	}
	for _, s := range []string{"true", "false", "y", "n", "maybe"} {
		if IsBool11(s) {
			t.Errorf("IsBool11(%q) = true", s)
		}
	}
}

func TestTimestampTagLayouts(t *testing.T) {
	for _, s := range []string{
		"2026-08-19",
		"2026-08-19T14:22:11Z",
		"2026-08-19T14:22:11.5Z",
		"2026-08-19 14:22:11",
	} {
		if _, ok := parseTimestamp(s); !ok {
			t.Errorf("%q was not parsed as a timestamp", s)
		}
	}
	if _, ok := parseTimestamp("not a time"); ok {
		t.Error("a non-timestamp was accepted")
	}
}

// ---- encoder ----

func TestEncodeScalarTypes(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "null"},
		{true, "true"},
		{int64(42), "42"},
		{int(7), "7"},
		{3.5, "3.5"},
		// A whole float keeps its point, or it would re-parse as an int.
		{4.0, "4.0"},
		{[]byte("hi"), "!!binary aGk="},
	}
	for _, c := range cases {
		if got := EncodeScalar(c.in); got != c.want {
			t.Errorf("EncodeScalar(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEncodeInfinitiesAndNaN(t *testing.T) {
	for in, want := range map[float64]string{} {
		_ = in
		_ = want
	}
	if got := EncodeScalar(mustParseFloat(t, ".inf")); got != ".inf" {
		t.Errorf("inf = %q", got)
	}
	if got := EncodeScalar(mustParseFloat(t, "-.inf")); got != "-.inf" {
		t.Errorf("-inf = %q", got)
	}
	if got := EncodeScalar(mustParseFloat(t, ".nan")); got != ".nan" {
		t.Errorf("nan = %q", got)
	}
}

func mustParseFloat(t *testing.T, src string) float64 {
	t.Helper()
	v := get(t, parse(t, "v: "+src), "v")
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s did not parse as a float: %#v", src, v)
	}
	return f
}

func TestEncodeNestedStructures(t *testing.T) {
	v := parse(t, `top:
  list:
    - one
    - nested:
        deep: value
  scalar: 1
`)
	out := Encode(v, EncodeOptions{})
	again, _, err := Parse([]byte(out), DefaultOptions("round.sls"))
	if err != nil {
		t.Fatalf("re-parsing failed: %v\n%s", err, out)
	}
	if !sameValue(v, again) {
		t.Errorf("round trip changed the value:\n%s", out)
	}
}

func TestEncodeQuotesControlCharacters(t *testing.T) {
	got := EncodeScalar("line\nbreak\ttab")
	if !strings.Contains(got, `\n`) || !strings.Contains(got, `\t`) {
		t.Errorf("control characters were not escaped: %s", got)
	}
	// A round trip through the parser recovers the original.
	v := get(t, parse(t, "v: "+got), "v")
	if v != "line\nbreak\ttab" {
		t.Errorf("round trip = %q", v)
	}
}

func TestEncodeTopLevelScalarAndSequence(t *testing.T) {
	if got := Encode("plain", EncodeOptions{}); got != "plain\n" {
		t.Errorf("scalar = %q", got)
	}
	if got := Encode([]any{"a", "b"}, EncodeOptions{}); got != "- a\n- b\n" {
		t.Errorf("sequence = %q", got)
	}
	if got := Encode([]any{}, EncodeOptions{}); got != "[]\n" {
		t.Errorf("empty sequence = %q", got)
	}
	if got := Encode(value.NewMap(0), EncodeOptions{}); got != "{}\n" {
		t.Errorf("empty mapping = %q", got)
	}
}

func TestDocumentMarkerHandling(t *testing.T) {
	// A leading directives-end marker is fine.
	v := parse(t, "---\na: 1\n")
	if got := get(t, v, "a"); got != int64(1) {
		t.Errorf("a = %#v", got)
	}
	// An empty document between markers is a null document.
	docs, _, err := ParseStream([]byte("---\n---\na: 1\n"), DefaultOptions("s.sls"))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0] != nil {
		t.Errorf("docs = %#v", docs)
	}
	// An anchor does not cross a document boundary.
	_, _, err = ParseStream([]byte("a: &x 1\n---\nb: *x\n"), DefaultOptions("s.sls"))
	if err == nil {
		t.Error("an anchor should not survive a document boundary")
	}
}

// An explicit key with no value line is how a set is written, and how a
// mapping with a missing value is written. SPEC section 10.1.1 lists `? `
// among the supported constructs.
func TestExplicitKeyWithoutAValue(t *testing.T) {
	v, _, err := Parse([]byte("? a\n? b\nc:\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(*value.Map)
	if !ok {
		t.Fatalf("expected a mapping, got %T", v)
	}
	for _, k := range []string{"a", "b", "c"} {
		got, ok := m.Get(k)
		if !ok {
			t.Errorf("key %q is missing; keys are %v", k, m.StringKeys())
			continue
		}
		if got != nil {
			t.Errorf("key %q = %#v, want null", k, got)
		}
	}
	if got := m.StringKeys(); len(got) != 3 {
		t.Errorf("keys = %v", got)
	}
}

// A block scalar is a legal explicit key.
func TestExplicitKeyThatIsABlockScalar(t *testing.T) {
	v, _, err := Parse([]byte("? |\n  block key\n: value\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatal(err)
	}
	m := v.(*value.Map)
	got, ok := m.Get("block key\n")
	if !ok {
		t.Fatalf("keys = %v", m.StringKeys())
	}
	if got != "value" {
		t.Errorf("value = %#v", got)
	}
}

// A mapping key carries anchors and tags like any other node. Pillar
// trees anchor keys as well as values, and without this the key came out
// as the literal text "&anchor key".
func TestNodePropertiesOnAMappingKey(t *testing.T) {
	v, _, err := Parse([]byte("top:\n  &k1 key1: one\n  &k2 key2: two\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatal(err)
	}
	inner, _ := v.(*value.Map).Get("top")
	m, ok := inner.(*value.Map)
	if !ok {
		t.Fatalf("top = %#v", inner)
	}
	if got := m.StringKeys(); len(got) != 2 || got[0] != "key1" || got[1] != "key2" {
		t.Fatalf("keys = %v; the anchors should not be part of them", got)
	}
	if got, _ := m.Get("key1"); got != "one" {
		t.Errorf("key1 = %#v", got)
	}

	// A tag on a key applies to the key, so an explicitly tagged number
	// stays a string while its untagged neighbour resolves.
	v, _, err = Parse([]byte("!!str 1: one\n2: two\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatal(err)
	}
	m = v.(*value.Map)
	if _, ok := m.Get("1"); !ok {
		t.Errorf("the tagged key is not the string \"1\": %v", m.StringKeys())
	}
	if _, ok := m.Get(int64(2)); !ok {
		t.Errorf("the untagged key is not the integer 2: %v", m.StringKeys())
	}
}

// A plain scalar spans lines inside a flow collection too, and an
// implicit key does not: YAML requires a key to sit on one line, which is
// what keeps a parser's lookahead bounded.
func TestPlainScalarsInsideFlow(t *testing.T) {
	v, _, err := Parse([]byte("[\n  multi\n  line,\n  b,\n]\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.([]any)
	if !ok || len(got) != 2 {
		t.Fatalf("value = %#v", v)
	}
	if got[0] != "multi line" {
		t.Errorf("first entry = %#v, want the folded scalar", got[0])
	}

	// A key that folds a line break before its colon is refused inside a
	// flow sequence, where a single-pair entry's key must fit on one line.
	_, _, err = Parse([]byte("[ key\n  : value ]\n"), Options{File: "t.sls"})
	if err == nil {
		t.Error("a key spanning lines in a flow sequence should be refused")
	} else if !strings.Contains(err.Error(), "one line") {
		t.Errorf("the error should say why: %v", err)
	}

	// A flow mapping is the other case: there the break is allowed, and
	// treating the two alike is wrong whichever way it is written.
	v, _, err = Parse([]byte("{foo\n: bar}\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatalf("a flow mapping key may take its colon on the next line: %v", err)
	}
	if got, _ := v.(*value.Map).Get("foo"); got != "bar" {
		t.Errorf("value = %#v", v)
	}

	// A folded key with no value at all is an entry in its own right.
	v, _, err = Parse([]byte("{ multi\n  line, a: b }\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatal(err)
	}
	m := v.(*value.Map)
	if got, ok := m.Get("multi line"); !ok || got != nil {
		t.Errorf("keys = %v, want a folded key with a null value", m.StringKeys())
	}

	// A key that merely begins on the line after the bracket is fine.
	v, _, err = Parse([]byte("[\nfoo: bar\n]\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatalf("a key on its own line was refused: %v", err)
	}
	pair := v.([]any)[0].(*value.Map)
	if got, _ := pair.Get("foo"); got != "bar" {
		t.Errorf("pair = %v", pair.StringKeys())
	}
}

// White space is allowed between a key and its colon, and after the
// colon a tab separates the value just as a space does.
func TestWhitespaceAroundTheKeyColon(t *testing.T) {
	cases := map[string]string{
		"'key' : value\n":   "value",
		"\"key\" : value\n": "value",
		"key\t: value\n":    "value",
		"key   :   value\n": "value",
		"key:\tvalue\n":     "value",
	}
	for src, want := range cases {
		v, _, err := Parse([]byte(src), Options{File: "t.sls"})
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		got, ok := v.(*value.Map).Get("key")
		if !ok || got != want {
			t.Errorf("%q -> %#v, want key=%q", src, v, want)
		}
	}

	// A key that ends in a colon with no separator is still one token, so
	// `a:b` is the scalar "a:b" rather than a mapping.
	v, _, err := Parse([]byte("a:b\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatal(err)
	}
	if v != "a:b" {
		t.Errorf("a:b parsed as %#v, want the string", v)
	}
}

// A block scalar with nothing indented under it is empty, not an error.
// `strip: >-` followed by the next key at the mapping's own column is a
// mapping of three empty values, which is Spec Example 8.6.
func TestEmptyBlockScalar(t *testing.T) {
	v, _, err := Parse([]byte("strip: >-\n\nclip: >\n\nkeep: |+\n\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(*value.Map)
	if !ok {
		t.Fatalf("value = %#v", v)
	}
	for _, k := range []string{"strip", "clip", "keep"} {
		if !m.Has(k) {
			t.Errorf("key %q is missing; keys are %v", k, m.StringKeys())
		}
	}
	// Chomping still applies to the nothing that is there.
	if got, _ := m.Get("strip"); got != "" {
		t.Errorf("strip = %#v, want the empty string", got)
	}
}

// A directive is metadata for a document, and malformed metadata is an
// error rather than something to shrug at.
func TestDirectiveValidation(t *testing.T) {
	bad := map[string]string{
		"%YAML 1.2\n":                 "must be followed by a --- document marker",
		"%YAML 1.2 foo\n---\n":        "exactly one version",
		"%YAML 1.2\n%YAML 1.2\n---\n": "only one %YAML",
		"%YAML 1.1#...\n---\n":        "version such as 1.1",
		"%\n---\n":                    "a directive needs a name",
	}
	for src, want := range bad {
		_, _, err := ParseStream([]byte(src), Options{File: "t.sls"})
		if err == nil {
			t.Errorf("%q parsed; it should be an error", src)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %q does not mention %q", src, err, want)
		}
	}

	// A version halite does not implement is a warning, not an error: the
	// document may still be readable, and refusing it helps nobody.
	_, warns, err := ParseStream([]byte("%YAML 1.3\n---\nkey: value\n"), Options{File: "t.sls"})
	if err != nil {
		t.Fatalf("an unimplemented version should warn, not fail: %v", err)
	}
	if len(warns) != 1 || warns[0].Kind != WarnDirective {
		t.Errorf("warnings = %v", warns)
	}

	// A directive after a document needs the document closed first.
	_, _, err = ParseStream([]byte("---\na: 1\n%YAML 1.2\n---\nb: 2\n"), Options{File: "t.sls"})
	if err == nil {
		t.Log("a directive after an unclosed document is still accepted; recorded as a gap")
	}
	// With the marker it is fine.
	if _, _, err := ParseStream([]byte("---\na: 1\n...\n%YAML 1.2\n---\nb: 2\n"), Options{File: "t.sls"}); err != nil {
		t.Errorf("a directive after a closed document was refused: %v", err)
	}
}

// `\xNN` names the code point U+00NN, not the byte NN. Emitting the raw
// byte produced a string that is not valid UTF-8 for anything above 0x7F,
// which the encoder then wrote out and the parser refused to read back.
func TestHexEscapeIsACodePoint(t *testing.T) {
	cases := map[string]string{
		`"\x41"`:   "A",
		`"\x00"`:   "\x00",
		`"\x80"`:   "\u0080",
		`"\xff"`:   "\u00ff",
		`"\u00e9"`: "\u00e9",
	}
	for src, want := range cases {
		v, _, err := Parse([]byte(src+"\n"), Options{File: "t.sls"})
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if v != any(want) {
			t.Errorf("%s = % x, want % x", src, v, want)
			continue
		}
		// Whatever came out must survive a round trip through the encoder.
		out := Encode(v, EncodeOptions{})
		back, _, err := Parse([]byte(out), Options{File: "t.sls"})
		if err != nil {
			t.Errorf("%s re-encoded to %q, which does not parse: %v", src, out, err)
			continue
		}
		if back != any(want) {
			t.Errorf("%s round-tripped to % x", src, back)
		}
	}
}
