package signature

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

func fileManaged() Signature {
	return Signature{
		Module: "file", Function: "managed",
		Params: []Param{
			{Name: "name", Type: Path, Required: true},
			{Name: "source", Type: String},
			{Name: "mode", Type: Mode},
			{Name: "user", Type: String, Default: "root"},
			{Name: "makedirs", Type: Bool, Default: false},
			{Name: "context", Type: Map},
			{Name: "backup", Type: String, Choices: []string{"local", "none"}},
		},
		Mutates: true, TestMode: TestReliable, Section: "15.2",
	}
}

func TestBindPositionalAndKeyword(t *testing.T) {
	s := fileManaged()
	out, errs := s.Bind([]any{"/etc/nginx.conf"}, value.MapOf("mode", "0644", "makedirs", true))
	if len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	if got, _ := out.Get("name"); got != "/etc/nginx.conf" {
		t.Errorf("name = %#v", got)
	}
	if got, _ := out.Get("mode"); got != "0644" {
		t.Errorf("mode = %#v", got)
	}
	if got, _ := out.Get("user"); got != "root" {
		t.Errorf("the default should be filled in, got %#v", got)
	}
}

func TestBindReportsEveryErrorAtOnce(t *testing.T) {
	// Reporting the first error and stopping is what makes fixing a large
	// tree an iterative grind; SPEC section 11.2 step 10 requires all of
	// them together.
	s := fileManaged()
	_, errs := s.Bind(nil, value.MapOf(
		"mode", int64(420),
		"makedirs", "yes-please",
		"nosuchparam", 1,
	))
	if len(errs) < 4 {
		t.Fatalf("got %d errors, want one for the missing name and one for each bad argument: %v", len(errs), errs)
	}
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	for _, want := range []string{"name", "is required", "mode", "makedirs", "nosuchparam"} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors do not mention %q:\n%s", want, joined)
		}
	}
}

// TestModeMustBeQuoted is the 0644-becomes-420 trap, caught at compile
// time with a message that says how to fix it.
func TestModeMustBeQuoted(t *testing.T) {
	s := fileManaged()
	_, errs := s.Bind([]any{"/etc/f"}, value.MapOf("mode", int64(420)))
	if len(errs) != 1 {
		t.Fatalf("errors = %v", errs)
	}
	msg := errs[0].Error()
	if !strings.Contains(msg, "quoted") || !strings.Contains(msg, "0644") {
		t.Errorf("the error should say how to write it: %s", msg)
	}
}

func TestChoicesAreEnforced(t *testing.T) {
	s := fileManaged()
	_, errs := s.Bind([]any{"/etc/f"}, value.MapOf("backup", "elsewhere"))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "must be one of") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestScalarWhereAListIsExpected(t *testing.T) {
	// Salt trees write `- names: web1` as often as they write a list.
	s := Signature{Module: "pkg", Function: "installed", Params: []Param{
		{Name: "pkgs", Type: List},
	}}
	out, errs := s.Bind(nil, value.MapOf("pkgs", "nginx"))
	if len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	got, _ := out.Get("pkgs")
	list, ok := got.([]any)
	if !ok || len(list) != 1 || list[0] != "nginx" {
		t.Errorf("pkgs = %#v, want a one-item list", got)
	}
}

func TestVariadicSoaksUpTheRest(t *testing.T) {
	s := Signature{Module: "cmd", Function: "run", Params: []Param{
		{Name: "cmd", Type: String, Required: true},
		{Name: "args", Type: List, Variadic: true},
	}, ArbitraryCode: true}
	out, errs := s.Bind([]any{"id", "-u", "-n"}, nil)
	if len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	got, _ := out.Get("args")
	if list, _ := got.([]any); len(list) != 2 {
		t.Errorf("args = %#v", got)
	}
}

func TestTooManyPositionalArguments(t *testing.T) {
	s := Signature{Module: "test", Function: "ping", Params: []Param{{Name: "a", Type: Any}}}
	_, errs := s.Bind([]any{1, 2, 3}, nil)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "at most 1 positional") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestDuplicateArgumentIsRejected(t *testing.T) {
	s := fileManaged()
	_, errs := s.Bind([]any{"/etc/f"}, value.MapOf("name", "/etc/g"))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "both positionally and by keyword") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestRegistryLookupAndInventory(t *testing.T) {
	r := NewRegistry()
	r.Add(fileManaged(), Signature{Module: "file", Function: "directory"}, Signature{Module: "pkg", Function: "installed"})

	if _, ok := r.Lookup("file.managed"); !ok {
		t.Error("file.managed missing")
	}
	if r.Has("file.nosuch") {
		t.Error("file.nosuch should not resolve")
	}
	if got := r.Modules(); len(got) != 2 || got[0] != "file" || got[1] != "pkg" {
		t.Errorf("modules = %v", got)
	}
	if got := r.Functions("file"); len(got) != 2 || got[0].Function != "directory" {
		t.Errorf("file functions = %v", got)
	}
}

func TestJSONShapeIsStable(t *testing.T) {
	r := NewRegistry()
	r.Add(fileManaged())
	b, err := value.EncodeJSON(r.JSON(), 0)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"file.managed"`, `"module":"file"`, `"function":"managed"`,
		`"mutates":true`, `"test_mode":"reliable"`, `"arbitrary_code":false`,
		`"type":"path"`, `"required":true`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("schema JSON is missing %s:\n%s", want, s)
		}
	}
}

func TestDescribeNamesTheDangerousFlags(t *testing.T) {
	s := Signature{
		Module: "cmd", Function: "run", Doc: "Run a command.",
		Params:        []Param{{Name: "cmd", Type: String, Required: true}},
		Mutates:       true,
		ArbitraryCode: true,
		TestMode:      TestUnreliable,
		Privileges:    []string{"root"},
	}
	out := s.Describe()
	for _, want := range []string{"cmd.run", "arbitrary code", "must be granted by name", "test mode is unreliable", "root"} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe is missing %q:\n%s", want, out)
		}
	}
}

// SPEC section 9.2: a command line argument arrives as a string, because
// the parser refuses to guess at its type. The declared type is what
// converts it, and without that every numeric parameter was unusable from
// `halite-node call`.
func TestDeclaredTypeConvertsACommandLineString(t *testing.T) {
	s := Signature{Module: "m", Function: "f", Params: []Param{
		{Name: "count", Type: Int},
		{Name: "ratio", Type: Float},
		{Name: "flag", Type: Bool},
		{Name: "version", Type: String},
	}}
	bound, errs := s.Bind(nil, value.MapOf(
		"count", "30", "ratio", "1.5", "flag", "yes", "version", "1.0"))
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if v, _ := bound.Get("count"); v != int64(30) {
		t.Errorf("count = %#v, want the integer 30", v)
	}
	if v, _ := bound.Get("ratio"); v != 1.5 {
		t.Errorf("ratio = %#v", v)
	}
	if v, _ := bound.Get("flag"); v != true {
		t.Errorf("flag = %#v", v)
	}
	// The property this must not break: a parameter declared String keeps
	// its string, so a package version of 1.0 does not become a float.
	if v, _ := bound.Get("version"); v != "1.0" {
		t.Errorf("version = %#v, want the string; SPEC 9.2 exists to stop 1.0 becoming a float", v)
	}
}

func TestAStringThatIsNotANumberIsStillAnError(t *testing.T) {
	s := Signature{Module: "m", Function: "f", Params: []Param{{Name: "count", Type: Int}}}
	if _, errs := s.Bind(nil, value.MapOf("count", "many")); len(errs) == 0 {
		t.Error("a string that is not a number should be refused, not coerced to zero")
	}
	// A float where an integer belongs is refused too, rather than
	// truncated: 1.5 retries is not 1 retry.
	if _, errs := s.Bind(nil, value.MapOf("count", "1.5")); len(errs) == 0 {
		t.Error("a fractional string should not become an integer")
	}
	b := Signature{Module: "m", Function: "f", Params: []Param{{Name: "on", Type: Bool}}}
	if _, errs := b.Bind(nil, value.MapOf("on", "maybe")); len(errs) == 0 {
		t.Error("a string that is not a boolean should be refused")
	}
}

func TestModeAdviceSuggestsWhatWasTyped(t *testing.T) {
	// YAML has destroyed the evidence by the time the value arrives, and
	// the two readings need opposite advice. `mode: 0644` is YAML 1.1
	// octal and arrives as 420, where %04o puts back what was typed;
	// `mode: 640` is plain decimal and arrives as 640, where %04o would
	// suggest 1200 — the octal of a number the author never wrote.
	cases := map[int64][]string{
		640: {"'0640'", "'1200'"},
		420: {"'0644'"},
		777: {"'0777'", "'1411'"},
	}
	for n, wants := range cases {
		got := modeAdvice(n)
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("modeAdvice(%d) = %q, should offer %s", n, got, want)
			}
		}
	}

	// A number with a digit above 7 can only be the second reading.
	if got := modeAdvice(888); !strings.Contains(got, "'1570'") || strings.Contains(got, "if you meant") {
		t.Errorf("modeAdvice(888) = %q, want the single octal spelling", got)
	}
}
