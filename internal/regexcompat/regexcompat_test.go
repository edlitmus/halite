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

func TestAConstructSpelledInsideACharacterClassIsLiteral(t *testing.T) {
	// A character class scan is a raw substring search with only an
	// escape check, so a construct spelling that appears inside
	// [...] — where it is a set of literal characters, not syntax — was
	// a false positive. Every one of the 11 constructs must be silent
	// when it only ever occurs inside a class.
	cases := []struct {
		pattern string
		name    string
	}{
		{`[(?=]`, "lookahead"},
		{`[(?!]`, "negative lookahead"},
		{`[(?<=]`, "lookbehind"},
		{`[(?<!]`, "negative lookbehind"},
		{`[(?>]`, "atomic group"},
		{`[(?R]`, "recursion"},
		{`[(?(]`, "conditional group"},
		{`[\G]`, "anchor to the end of the previous match"},
		{`[\K]`, "match reset"},
		{`[\1]`, "backreference"},
		{`[\k<]`, "named backreference"},
	}
	for _, c := range cases {
		if got := Unsupported(c.pattern); len(got) != 0 {
			t.Errorf("%s: %q inside a class was wrongly refused: %v", c.name, c.pattern, got)
		}
	}

	// The paren-based constructs, once literal inside a class, are also
	// ordinary regexps RE2 can compile. The backslash-based ones (\G,
	// \K, \1, \k) are not asserted the same way here: RE2 itself refuses
	// an unrecognized escape inside a class regardless of this package,
	// so Compile failing on those is Go's own syntax check, not a false
	// positive from Unsupported.
	for _, p := range []string{`[(?=]`, `[(?!]`, `[(?<=]`, `[(?<!]`, `[(?>]`, `[(?R]`, `[(?(]`} {
		if _, err := Compile(p); err != nil {
			t.Errorf("%q inside a class should compile, got %v", p, err)
		}
	}
}

func TestTheSameConstructOutsideAClassIsStillRefused(t *testing.T) {
	// The fix for the class false positive must not swing the other way:
	// a construct that is genuinely outside a class must still be
	// caught. This is the direction where a regression is expensive, so
	// it gets its own test alongside TestEveryUnsupportedConstructIsNamed.
	cases := []struct {
		pattern string
		name    string
	}{
		{`a(?=b)[xyz]`, "lookahead"},
		{`[xyz](?!b)`, "negative lookahead"},
		{`(?<=a)[xyz]`, "lookbehind"},
		{`[xyz](?<!a)`, "negative lookbehind"},
		{`(?>a)[xyz]`, "atomic group"},
		{`[xyz]\((?R)\)`, "recursion"},
		{`(a)(?(1)[xyz]|c)`, "conditional group"},
		{`[xyz]\Ga`, "anchor to the end of the previous match"},
		{`a\K[xyz]b`, "match reset"},
		{`(a)[xyz]\1`, "backreference"},
		{`(?P<x>a)[xyz]\k<x>`, "named backreference"},
	}
	for _, c := range cases {
		found := Unsupported(c.pattern)
		if len(found) == 0 {
			t.Errorf("%s: %q was wrongly accepted once a class was added nearby", c.name, c.pattern)
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
	}
}

func TestALiteralCloseBracketAsTheFirstClassCharacter(t *testing.T) {
	// "]" as the first character of a class (or the first after a
	// leading "^") is a literal "]", not the close. Getting this wrong
	// is the classic off-by-one in bracket-expression parsing.
	cases := []struct {
		name    string
		pattern string
	}{
		{"literal ] first in the class", `[](?=]`},
		{"literal ] first after a negating ^", `[^](?=]`},
	}
	for _, c := range cases {
		if got := Unsupported(c.pattern); len(got) != 0 {
			t.Errorf("%s: %q was wrongly refused: %v", c.name, c.pattern, got)
		}
		if _, err := Compile(c.pattern); err != nil {
			t.Errorf("%s: %q should compile, got %v", c.name, c.pattern, err)
		}
	}
}

func TestAnEscapedBracketDoesNotOpenOrCloseAClass(t *testing.T) {
	// `\[` outside a class is a literal bracket, not the start of one, so
	// the "(?=" that follows is a real lookahead and must be refused.
	if got := Unsupported(`\[(?=]`); len(got) == 0 {
		t.Error(`\[(?= should still be refused: the "[" is escaped, so no class was opened`)
	}

	// `\]` inside a class is a literal bracket, not the close, so the
	// "(?=" that follows is still inside the class and must be silent.
	if got := Unsupported(`[\](?=]`); len(got) != 0 {
		t.Errorf(`[\](?=] should be accepted: the escaped "]" does not close the class, got %v`, got)
	}
}

func TestAPosixClassDoesNotConfuseTheBracketNesting(t *testing.T) {
	// The inner "[:" of a POSIX class like [[:alpha:]] is not a nested
	// class, and its own "]" (the one in ":]") must not be read as the
	// close of the outer class.
	if got := Unsupported(`[[:alpha:]]`); len(got) != 0 {
		t.Errorf("[[:alpha:]] was wrongly refused: %v", got)
	}
	if _, err := Compile(`[[:alpha:]]+`); err != nil {
		t.Errorf("[[:alpha:]]+ did not compile: %v", err)
	}

	// A construct spelling placed after a POSIX class, still inside the
	// outer bracket expression, must remain silent.
	if got := Unsupported(`[[:alpha:](?=]`); len(got) != 0 {
		t.Errorf("[[:alpha:](?=] was wrongly refused: %v", got)
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
