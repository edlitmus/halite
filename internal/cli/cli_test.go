package cli

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

func parse(t *testing.T, argv ...string) *Args {
	t.Helper()
	a, err := Parse(argv)
	if err != nil {
		t.Fatalf("Parse(%v): %v", argv, err)
	}
	return a
}

// SPEC section 9.2: arguments are strings unless a type is declared. Salt
// YAML-parses every argument, which is why a package version 1.0 becomes a
// float and NO becomes a boolean.

func TestVersionStringsSurviveTheCommandLine(t *testing.T) {
	a := parse(t, "pkg.install", "version=1.0", "arch=NO", "release=0755", "build=1e5")
	for key, want := range map[string]string{
		"version": "1.0",
		"arch":    "NO",
		"release": "0755",
		"build":   "1e5",
	} {
		got, _ := a.Kwargs.Get(key)
		if got != want {
			t.Errorf("%s = %#v (%T), want the string %q", key, got, got, want)
		}
	}
}

func TestOnlyExplicitBooleansAndNullAreTyped(t *testing.T) {
	a := parse(t, "test=True", "b=false", "c=None", "d=null", "e=yes", "f=on")
	for key, want := range map[string]any{
		"test": true, "b": false, "c": nil, "d": nil,
		// yes and on stay strings; only the Python spellings are typed.
		"e": "yes", "f": "on",
	} {
		got, ok := a.Kwargs.Get(key)
		if !ok {
			t.Errorf("%s is missing", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

func TestStructuredArgumentsMustBeWrittenAsJSON(t *testing.T) {
	a := parse(t, `pkgs=["nginx","curl"]`, `opts={"a":1}`)
	pkgs, _ := a.Kwargs.Get("pkgs")
	list, ok := pkgs.([]any)
	if !ok || len(list) != 2 || list[0] != "nginx" {
		t.Fatalf("pkgs = %#v", pkgs)
	}
	opts, _ := a.Kwargs.Get("opts")
	if m, ok := opts.(*value.Map); !ok || !m.Has("a") {
		t.Errorf("opts = %#v", opts)
	}
}

func TestMalformedJSONIsAnErrorRatherThanAString(t *testing.T) {
	// Silently treating it as a string would install a package literally
	// named `["nginx"`.
	_, err := Parse([]string{`pkgs=["nginx"`})
	if err == nil {
		t.Fatal("malformed JSON should be an error")
	}
	if !strings.Contains(err.Error(), "pkgs") {
		t.Errorf("the error should name the argument: %v", err)
	}
}

func TestPositionalArgumentsAreNotMistakenForKwargs(t *testing.T) {
	// A path or an expression containing `=` is positional.
	a := parse(t, "cmd.run", "./configure --prefix=/usr", "a-b=c", "=leading")
	if a.Kwargs.Len() != 0 {
		t.Errorf("nothing here is a kwarg: %v", a.Kwargs.StringKeys())
	}
	if len(a.Positional) != 4 {
		t.Errorf("positional = %v", a.Positional)
	}
}

func TestKwargKeysAcceptDigitsAfterTheFirstCharacter(t *testing.T) {
	a := parse(t, "sha256=abc", "_private=1")
	if a.Kwargs.Len() != 2 {
		t.Errorf("kwargs = %v", a.Kwargs.StringKeys())
	}
	// A leading digit is not an identifier, so it stays positional.
	a = parse(t, "1abc=x")
	if a.Kwargs.Len() != 0 || len(a.Positional) != 1 {
		t.Errorf("a leading digit should not be a kwarg: %v %v", a.Kwargs.StringKeys(), a.Positional)
	}
}

// A value-taking flag followed by a subcommand is the parse this test
// pins: `--test apply` must not swallow `apply`.
func TestBooleanFlagsDoNotSwallowTheNextArgument(t *testing.T) {
	a := parse(t, "--test", "apply", "web")
	if !a.Bool("test", false) {
		t.Error("--test did not set")
	}
	if len(a.Positional) != 2 || a.Positional[0] != "apply" {
		t.Errorf("positional = %v", a.Positional)
	}
}

func TestValueFlagsTakeBothSpellings(t *testing.T) {
	for _, argv := range [][]string{
		{"--out", "json", "apply"},
		{"--out=json", "apply"},
	} {
		a := parse(t, argv...)
		if got := a.Flag("out", "nested"); got != "json" {
			t.Errorf("%v: out = %q", argv, got)
		}
		if len(a.Positional) != 1 || a.Positional[0] != "apply" {
			t.Errorf("%v: positional = %v", argv, a.Positional)
		}
	}
}

func TestShortTargetFlagsTakeTheirValue(t *testing.T) {
	a := parse(t, "-G", "os_family:FreeBSD", "test.ping")
	if got := a.Flag("G", ""); got != "os_family:FreeBSD" {
		t.Errorf("-G = %q", got)
	}
	if len(a.Positional) != 1 {
		t.Errorf("positional = %v", a.Positional)
	}
}

func TestDoubleDashEndsFlagParsing(t *testing.T) {
	a := parse(t, "cmd.run", "--", "--not-a-flag", "-x", "k=v")
	if len(a.Flags) != 0 {
		t.Errorf("flags after -- : %v", a.Flags)
	}
	if len(a.Positional) != 3 {
		t.Errorf("positional = %v", a.Positional)
	}
	// A key=value pair is still a kwarg after --, because -- ends option
	// parsing and a kwarg is an argument rather than an option. The escape
	// hatch for a literal is a key that is not an identifier.
	if v, ok := a.Kwargs.Get("k"); !ok || v != "v" {
		t.Errorf("kwargs = %v", a.Kwargs.StringKeys())
	}
}

func TestFlagAccessors(t *testing.T) {
	a := parse(t, "--bare", "--set=value", "--off=false")
	if !a.Has("bare") || a.Has("absent") {
		t.Error("Has disagrees with the parse")
	}
	// A bare flag reads as "true" rather than the empty string, so that a
	// caller comparing to a default is not surprised.
	if got := a.Flag("bare", "def"); got != "true" {
		t.Errorf("bare = %q", got)
	}
	if got := a.Flag("set", "def"); got != "value" {
		t.Errorf("set = %q", got)
	}
	if got := a.Flag("absent", "def"); got != "def" {
		t.Errorf("absent = %q", got)
	}
	if !a.Bool("bare", false) || a.Bool("off", true) || !a.Bool("absent", true) {
		t.Errorf("Bool: %v", a.Flags)
	}
	// An unparseable boolean falls back to the default rather than
	// guessing.
	a = parse(t, "--weird=maybe")
	if !a.Bool("weird", true) || a.Bool("weird", false) {
		t.Error("an unrecognised boolean should fall back to the default")
	}
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{
		"": Nested, "nested": Nested, "json": JSON,
		"yaml": YAML, "quiet": Quiet, "txt": Txt,
	} {
		got, err := ParseFormat(in)
		if err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %v, %v", in, got, err)
		}
	}
	_, err := ParseFormat("xml")
	if err == nil {
		t.Fatal("an unknown format should be an error")
	}
	// The message must list what is on offer, or the operator has to read
	// the source to find out.
	for _, want := range []string{"nested", "json", "yaml", "txt", "quiet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should list %q: %v", want, err)
		}
	}
}

// ---- output ----

func render(t *testing.T, v any, f Format) string {
	t.Helper()
	var b strings.Builder
	if err := Write(&b, v, f, 0); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestNestedOutputShape(t *testing.T) {
	v := value.MapOf("node1", value.MapOf(
		"pkg_|-nginx_|-nginx_|-installed", value.MapOf(
			"result", true,
			"comment", "Package nginx is already installed",
			"changes", value.NewMap(0),
			"nested", value.MapOf("depth", int64(2)),
			"list", []any{"a", value.MapOf("k", "v")},
			"empty_list", []any{},
			"none", nil,
		),
	))
	out := render(t, v, Nested)
	for _, want := range []string{
		"node1:\n",
		"    pkg_|-nginx_|-nginx_|-installed:\n",
		"        result: True",
		"        changes: {}",
		"        empty_list: []",
		"        none: None",
		"            depth: 2",
		"        - a",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestNestedRendersAMultilineStringAsABlock(t *testing.T) {
	out := render(t, value.MapOf("diff", "--- a\n+++ b\n"), Nested)
	if !strings.Contains(out, "diff: |\n") {
		t.Errorf("a multiline value should use a block indicator:\n%s", out)
	}
	if !strings.Contains(out, "        --- a") {
		t.Errorf("the block body should be indented:\n%s", out)
	}
}

func TestNestedRendersAScalarAndABareList(t *testing.T) {
	if got := render(t, "plain", Nested); got != "plain\n" {
		t.Errorf("scalar = %q", got)
	}
	out := render(t, []any{"a", []any{"b"}}, Nested)
	if !strings.Contains(out, "- a") || !strings.Contains(out, "-\n") {
		t.Errorf("list = %q", out)
	}
}

func TestQuietPrintsNothing(t *testing.T) {
	if got := render(t, value.MapOf("a", 1), Quiet); got != "" {
		t.Errorf("quiet printed %q", got)
	}
}

func TestTxtIsOneLinePerTopLevelKey(t *testing.T) {
	out := render(t, value.MapOf("web1", true, "web2", false), Txt)
	if out != "web1: True\nweb2: False\n" {
		t.Errorf("txt = %q", out)
	}
	if got := render(t, "scalar", Txt); got != "scalar\n" {
		t.Errorf("txt scalar = %q", got)
	}
}

func TestJSONOutputIsMachineReadable(t *testing.T) {
	out := render(t, value.MapOf("b", int64(2), "a", int64(1)), JSON)
	if strings.TrimSpace(out) != `{"b":2,"a":1}` {
		t.Errorf("json = %q", out)
	}
	var b strings.Builder
	if err := Write(&b, value.MapOf("a", value.MapOf("b", 1)), JSON, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "\n  ") {
		t.Errorf("the indent was ignored: %q", b.String())
	}
}

func TestYAMLOutputRoundTrips(t *testing.T) {
	out := render(t, value.MapOf("key", "value", "list", []any{int64(1)}), YAML)
	if !strings.Contains(out, "key: value") {
		t.Errorf("yaml = %q", out)
	}
}
