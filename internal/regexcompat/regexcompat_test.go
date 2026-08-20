package regexcompat

import (
	"strings"
	"testing"
)

// SPEC section 10.4: an unsupported construct is a hard error naming the
// construct. A silent non-match in file.replace is a state that reports
// success and changes nothing, which is the outcome this package exists to
// prevent.

func TestEveryUnsupportedConstructIsNamed(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
	}{
		{`foo(?=bar)`, "lookahead"},
		{`foo(?!bar)`, "negative lookahead"},
		{`(?<=foo)bar`, "lookbehind"},
		{`(?<!foo)bar`, "negative lookbehind"},
		{`(?>foo)`, "atomic group"},
		{`\((?R)\)`, "recursion"},
		{`(a)(?(1)b|c)`, "conditional group"},
		{`\Gfoo`, "anchor to the end of the previous match"},
		{`foo\Kbar`, "match reset"},
		{`(a)\1`, "backreference"},
		{`(?P<x>a)\k<x>`, "named backreference"},
		{`(?P<x>a)\k'x'`, "named backreference"},
	}
	for _, c := range cases {
		found := Unsupported(c.pattern)
		if len(found) == 0 {
			t.Errorf("%s: %q was accepted", c.name, c.pattern)
			continue
		}
		named := false
		for _, f := range found {
			if f.Name == c.name {
				named = true
			}
		}
		if !named {
			t.Errorf("%q reported %v, none named %q", c.pattern, found, c.name)
		}

		// Compile must refuse it, and the message must be actionable.
		re, err := Compile(c.pattern)
		if err == nil {
			t.Errorf("%q compiled; a silent non-match is the failure mode this prevents", c.pattern)
			continue
		}
		if re != nil {
			t.Errorf("%q returned a regexp alongside an error", c.pattern)
		}
		msg := err.Error()
		for _, want := range []string{c.name, "SPEC section 10.4"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%q error is missing %q:\n%s", c.pattern, want, msg)
			}
		}
	}
}

func TestEveryConstructOffersAWorkaround(t *testing.T) {
	// A refusal with no suggested migration just moves the problem.
	for _, p := range []string{`a(?=b)`, `(a)\1`, `(?<=a)b`, `a\Kb`, `(?>a)`, `(?(1)a)`, `\Ga`, `a(?!b)`, `(?<!a)b`, `(?R)`} {
		for _, c := range Unsupported(p) {
			if c.Workaround == "" {
				t.Errorf("%q: construct %s has no workaround", p, c.Syntax)
			}
			if !strings.Contains(c.String(), c.Syntax) {
				t.Errorf("String() should name the syntax: %q", c.String())
			}
		}
	}
}

func TestPatternsRE2SupportsAreAccepted(t *testing.T) {
	// The common shapes a carried-over Salt tree actually contains.
	for _, p := range []string{
		`^\s*#?\s*PermitRootLogin\s+.*$`,
		`(?i)^listen\s+[0-9]+`,
		`^(web|db)[0-9]{2}\.example\.com$`,
		`\d+\.\d+\.\d+\.\d+`,
		`(?P<key>[a-z_]+)\s*=\s*(?P<val>.*)`,
		`[[:alpha:]]+`,
		`a{2,4}`,
		`\\1`, // an escaped backslash then a literal 1, not a backreference
		`\\\\`,
	} {
		if got := Unsupported(p); len(got) != 0 {
			t.Errorf("%q was wrongly refused: %v", p, got)
		}
		if _, err := Compile(p); err != nil {
			t.Errorf("%q did not compile: %v", p, err)
		}
	}
}

func TestAnEscapedConstructIsALiteral(t *testing.T) {
	// `\(?=` is a literal paren followed by an optional `?`. Refusing it
	// would be a false positive that blocks a valid migration.
	if got := Unsupported(`\(?=x`); len(got) != 0 {
		t.Errorf(`\(?= should be literal, got %v`, got)
	}
	// But a doubled backslash re-arms the construct.
	if got := Unsupported(`\\(?=x`); len(got) == 0 {
		t.Error(`\\(?= is a real lookahead and should be refused`)
	}
}

func TestEveryOccurrenceIsReportedWithItsOffset(t *testing.T) {
	found := Unsupported(`(?=a)x(?=b)`)
	if len(found) != 2 {
		t.Fatalf("expected both lookaheads, got %v", found)
	}
	if found[0].Offset != 0 || found[1].Offset != 6 {
		t.Errorf("offsets = %d, %d", found[0].Offset, found[1].Offset)
	}
}

func TestAllNineBackreferencesAreCaught(t *testing.T) {
	for _, d := range "123456789" {
		p := `(a)\` + string(d)
		if got := Unsupported(p); len(got) == 0 {
			t.Errorf("%q was accepted", p)
		}
	}
	// \0 is not a backreference in Python either; it is a null byte.
	if got := Unsupported(`(a)\0`); len(got) != 0 {
		t.Errorf(`\0 should not be read as a backreference: %v`, got)
	}
}

func TestASyntaxErrorIsReportedAsItself(t *testing.T) {
	// A pattern RE2 rejects for its own reasons is not an unsupported
	// construct, and saying so would send the reader down the wrong path.
	re, err := Compile(`a(b`)
	if err == nil {
		t.Fatalf("an unbalanced paren compiled: %v", re)
	}
	var e *Error
	if !asCompatError(err, &e) {
		t.Fatalf("error type = %T", err)
	}
	if len(e.Constructs) != 0 {
		t.Errorf("a syntax error should report no constructs: %v", e.Constructs)
	}
	if e.Cause == nil {
		t.Error("a syntax error should carry its cause for errors.Unwrap")
	}
	if !strings.Contains(err.Error(), "invalid regular expression") {
		t.Errorf("message = %v", err)
	}
	if e.Unwrap() != e.Cause {
		t.Error("Unwrap should return the cause")
	}
}

func TestCompileWithFlags(t *testing.T) {
	re, err := CompileWithFlags(`^abc$`, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("x\nABC\ny") {
		t.Error("ignoreCase with multiline did not take effect")
	}

	re, err = CompileWithFlags(`a.c`, false, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("a\nc") {
		t.Error("dotAll did not take effect")
	}

	// No flags leaves the pattern alone.
	re, err = CompileWithFlags(`^abc$`, false, false, false)
	if err != nil || re.MatchString("ABC") {
		t.Errorf("unflagged pattern = %v, %v", re, err)
	}

	// The flags do not smuggle an unsupported construct past the check.
	if _, err := CompileWithFlags(`a(?=b)`, true, false, false); err == nil {
		t.Error("flags should not bypass the construct check")
	}
}

func asCompatError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
