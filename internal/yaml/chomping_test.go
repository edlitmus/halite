package yaml

import (
	"sort"
	"strings"
	"testing"
)

// Block scalar chomping, measured rather than assumed.
//
// SPEC 10.1.1 names chomping as the construct that has to be exactly
// right, because a block scalar is what `file.managed` contents is
// written as: a missing or extra trailing newline is a file that differs
// from the one the state describes, and a state that reports a change on
// every run.
//
// Two things are checked here, and the second is why this file exists
// rather than a handful of cases in parse_test.go:
//
//   - `blockScalarCases` is a matrix -- two styles, three chomping
//     indicators, and the content shapes that decide what the indicator
//     acts on -- so the coverage is a product rather than a list somebody
//     thought of.
//   - every case in it is in the PyYAML differential of
//     `differential_test.go`, so the expectation comes from the reference
//     implementation rather than from a reading of the specification.
//     `chompExpect` below is what PyYAML actually answered, captured, and
//     the differential re-derives it wherever PyYAML is installed.
//
// The captured table is what makes this run on a machine with no Python.
// It is not a substitute for the differential: a fixture written from
// expectation is the mistake DIVERGENCE 5.31 is about, and the guard
// against it here is that the two tables are the same case list, and that
// the differential fails the moment a captured value stops matching the
// reference.

// blockScalarCases is the chomping matrix.
//
// The shapes are the ones the chomping indicator behaves differently on:
// what it does to the last line break depends on whether there is content
// after it, whether the trailing lines are blank, whether the scalar ends
// the file, and whether the scalar is a mapping value, a sequence entry,
// or the whole document.
func blockScalarCases() []diffCase {
	var cases []diffCase

	// shapes take the block header -- "|", ">-", "|2+" and so on -- and
	// return the document to parse.
	shapes := []struct {
		name string
		src  func(header string) string
	}{
		{"plain", func(h string) string { return "k: " + h + "\n  one\n  two\n" }},
		{"trailing-blank", func(h string) string { return "k: " + h + "\n  one\n\n" }},
		{"trailing-blanks", func(h string) string { return "k: " + h + "\n  one\n\n\n\n" }},
		{"no-final-break", func(h string) string { return "k: " + h + "\n  one" }},
		{"no-final-break-two-lines", func(h string) string { return "k: " + h + "\n  one\n  two" }},
		{"trailing-space-eof", func(h string) string { return "k: " + h + "\n  one\n  " }},
		{"deeper-space-eof", func(h string) string { return "k: " + h + "\n  one\n   " }},
		{"content-space-eof", func(h string) string { return "k: " + h + "\n  one  " }},
		{"blank-then-eof", func(h string) string { return "k: " + h + "\n  one\n\n  two" }},
		{"empty", func(h string) string { return "k: " + h + "\nj: 1\n" }},
		{"blank-only", func(h string) string { return "k: " + h + "\n\n\n" }},
		{"interior-blank", func(h string) string { return "k: " + h + "\n  one\n\n  two\n" }},
		{"more-indented", func(h string) string { return "k: " + h + "\n  one\n   two\n  three\n" }},
		{"trailing-space", func(h string) string { return "k: " + h + "\n  one  \n" }},
		// A tab at the head of the content, which folding treats as a
		// more-indented line. It is also the one shape here where the
		// reference does not speak with one voice: pure Python PyYAML
		// reads it, libyaml refuses it as "a tab character where an
		// indentation space is expected", and Salt picks libyaml
		// whenever it is installed. So a tree that writes one is not
		// portable between two Salt installations either. halite reads
		// it, with the lenient direction taken deliberately, as
		// everywhere else a document divides them.
		{"leading-tab", func(h string) string { return "k: " + h + "\n  \tone\n  two\n" }},
		{"followed-by-key", func(h string) string { return "k: " + h + "\n  one\n\nj: 1\n" }},
		{"crlf", func(h string) string { return "k: " + h + "\r\n  one\r\n  two\r\n" }},
		{"in-sequence", func(h string) string { return "- " + h + "\n  one\n  two\n" }},
		{"document-root", func(h string) string { return "--- " + h + "\n one\n two\n" }},
	}

	for _, style := range []struct{ name, ind string }{{"literal", "|"}, {"folded", ">"}} {
		for _, chomp := range []struct{ name, ind string }{{"clip", ""}, {"strip", "-"}, {"keep", "+"}} {
			for _, shape := range shapes {
				cases = append(cases, diffCase{
					name: "chomp/" + style.name + "-" + chomp.name + "-" + shape.name,
					src:  shape.src(style.ind + chomp.ind),
				})
			}
			// An explicit indentation indicator is written with the
			// chomping indicator on either side of it, and both spellings
			// are legal. The content below sits two columns in, with a
			// more-indented line under it, so the indicator decides how
			// much of each line is content and chomping then acts on
			// what is left.
			body := "\n  one\n   two\n"
			cases = append(cases,
				diffCase{"chomp/" + style.name + "-" + chomp.name + "-indent-first",
					"k: " + style.ind + "2" + chomp.ind + body},
				diffCase{"chomp/" + style.name + "-" + chomp.name + "-indent-last",
					"k: " + style.ind + chomp.ind + "2" + body},
			)
		}
	}
	return cases
}

// TestBlockScalarChompingMatchesTheReference holds the parser to what
// PyYAML answered for every case in the matrix.
//
// Where PyYAML is installed, `TestPyYAMLDifferential` checks the same
// documents against the live implementation and fails on any difference,
// captured table or not. This test is the half that runs everywhere else.
func TestBlockScalarChompingMatchesTheReference(t *testing.T) {
	cases := blockScalarCases()
	if len(cases) != len(chompExpect) {
		t.Errorf("the matrix has %d cases and the captured table %d rows; "+
			"regenerate it with HALITE_YAML_CHOMP_REGEN=1", len(cases), len(chompExpect))
	}

	seen := map[string]bool{}
	for _, c := range cases {
		want, ok := chompExpect[c.name]
		if !ok {
			t.Errorf("%s has no captured value.\n  document: %q\n"+
				"Capture one with HALITE_YAML_CHOMP_REGEN=1 and an interpreter that has PyYAML.",
				c.name, c.src)
			continue
		}
		seen[c.name] = true

		got := "error"
		if v, _, err := Parse([]byte(c.src), DefaultOptions(c.name)); err == nil {
			got = diffShape(v)
		}
		if got != want {
			t.Errorf("%s\n  document: %q\n  halite:   %s\n  PyYAML:   %s",
				c.name, c.src, got, want)
		}
	}
	for name := range chompExpect {
		if !seen[name] {
			t.Errorf("the captured table has a row for %s, which the matrix no longer produces; "+
				"a stale row hides the next real difference", name)
		}
	}
}

// regenChompTable prints the captured table from what PyYAML answers now.
// TestPyYAMLDifferential calls it under HALITE_YAML_CHOMP_REGEN.
func regenChompTable(t *testing.T, cases []diffCase, theirs []pyResult) {
	t.Helper()
	byName := map[string]string{}
	for i, c := range cases {
		if !strings.HasPrefix(c.name, "chomp/") {
			continue
		}
		shape := theirs[i].Shape
		if theirs[i].Err != "" {
			shape = "error"
		}
		byName[c.name] = shape
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("var chompExpect = map[string]string{\n")
	for _, n := range names {
		b.WriteString("\t" + quoteGo(n) + ": " + quoteGo(byName[n]) + ",\n")
	}
	b.WriteString("}\n")
	t.Log("\n" + b.String())
}

func quoteGo(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// chompExpect is what PyYAML 6.0.3 answered for every case in the
// matrix, captured on 2026-09-11 and regenerated with:
//
//	HALITE_PYYAML_PYTHON=python3.11 HALITE_YAML_CHOMP_REGEN=1 \
//		go test ./internal/yaml/ -run TestPyYAMLDifferential -v
//
// The values are in the shape grammar of diffShape, so a row records the
// resolved type as well as the text.
var chompExpect = map[string]string{
	"chomp/folded-clip-blank-only":                 "{str:k=str:}",
	"chomp/folded-clip-blank-then-eof":             "{str:k=str:one\ntwo}",
	"chomp/folded-clip-content-space-eof":          "{str:k=str:one  }",
	"chomp/folded-clip-crlf":                       "{str:k=str:one two\n}",
	"chomp/folded-clip-deeper-space-eof":           "{str:k=str:one\n }",
	"chomp/folded-clip-document-root":              "str:one two\n",
	"chomp/folded-clip-empty":                      "{str:j=int:1,str:k=str:}",
	"chomp/folded-clip-followed-by-key":            "{str:j=int:1,str:k=str:one\n}",
	"chomp/folded-clip-in-sequence":                "[str:one two\n]",
	"chomp/folded-clip-indent-first":               "{str:k=str:one\n two\n}",
	"chomp/folded-clip-indent-last":                "{str:k=str:one\n two\n}",
	"chomp/folded-clip-interior-blank":             "{str:k=str:one\ntwo\n}",
	"chomp/folded-clip-leading-tab":                "{str:k=str:\tone\ntwo\n}",
	"chomp/folded-clip-more-indented":              "{str:k=str:one\n two\nthree\n}",
	"chomp/folded-clip-no-final-break":             "{str:k=str:one}",
	"chomp/folded-clip-no-final-break-two-lines":   "{str:k=str:one two}",
	"chomp/folded-clip-plain":                      "{str:k=str:one two\n}",
	"chomp/folded-clip-trailing-blank":             "{str:k=str:one\n}",
	"chomp/folded-clip-trailing-blanks":            "{str:k=str:one\n}",
	"chomp/folded-clip-trailing-space":             "{str:k=str:one  \n}",
	"chomp/folded-clip-trailing-space-eof":         "{str:k=str:one\n}",
	"chomp/folded-keep-blank-only":                 "{str:k=str:\n\n}",
	"chomp/folded-keep-blank-then-eof":             "{str:k=str:one\ntwo}",
	"chomp/folded-keep-content-space-eof":          "{str:k=str:one  }",
	"chomp/folded-keep-crlf":                       "{str:k=str:one two\n}",
	"chomp/folded-keep-deeper-space-eof":           "{str:k=str:one\n }",
	"chomp/folded-keep-document-root":              "str:one two\n",
	"chomp/folded-keep-empty":                      "{str:j=int:1,str:k=str:}",
	"chomp/folded-keep-followed-by-key":            "{str:j=int:1,str:k=str:one\n\n}",
	"chomp/folded-keep-in-sequence":                "[str:one two\n]",
	"chomp/folded-keep-indent-first":               "{str:k=str:one\n two\n}",
	"chomp/folded-keep-indent-last":                "{str:k=str:one\n two\n}",
	"chomp/folded-keep-interior-blank":             "{str:k=str:one\ntwo\n}",
	"chomp/folded-keep-leading-tab":                "{str:k=str:\tone\ntwo\n}",
	"chomp/folded-keep-more-indented":              "{str:k=str:one\n two\nthree\n}",
	"chomp/folded-keep-no-final-break":             "{str:k=str:one}",
	"chomp/folded-keep-no-final-break-two-lines":   "{str:k=str:one two}",
	"chomp/folded-keep-plain":                      "{str:k=str:one two\n}",
	"chomp/folded-keep-trailing-blank":             "{str:k=str:one\n\n}",
	"chomp/folded-keep-trailing-blanks":            "{str:k=str:one\n\n\n\n}",
	"chomp/folded-keep-trailing-space":             "{str:k=str:one  \n}",
	"chomp/folded-keep-trailing-space-eof":         "{str:k=str:one\n}",
	"chomp/folded-strip-blank-only":                "{str:k=str:}",
	"chomp/folded-strip-blank-then-eof":            "{str:k=str:one\ntwo}",
	"chomp/folded-strip-content-space-eof":         "{str:k=str:one  }",
	"chomp/folded-strip-crlf":                      "{str:k=str:one two}",
	"chomp/folded-strip-deeper-space-eof":          "{str:k=str:one\n }",
	"chomp/folded-strip-document-root":             "str:one two",
	"chomp/folded-strip-empty":                     "{str:j=int:1,str:k=str:}",
	"chomp/folded-strip-followed-by-key":           "{str:j=int:1,str:k=str:one}",
	"chomp/folded-strip-in-sequence":               "[str:one two]",
	"chomp/folded-strip-indent-first":              "{str:k=str:one\n two}",
	"chomp/folded-strip-indent-last":               "{str:k=str:one\n two}",
	"chomp/folded-strip-interior-blank":            "{str:k=str:one\ntwo}",
	"chomp/folded-strip-leading-tab":               "{str:k=str:\tone\ntwo}",
	"chomp/folded-strip-more-indented":             "{str:k=str:one\n two\nthree}",
	"chomp/folded-strip-no-final-break":            "{str:k=str:one}",
	"chomp/folded-strip-no-final-break-two-lines":  "{str:k=str:one two}",
	"chomp/folded-strip-plain":                     "{str:k=str:one two}",
	"chomp/folded-strip-trailing-blank":            "{str:k=str:one}",
	"chomp/folded-strip-trailing-blanks":           "{str:k=str:one}",
	"chomp/folded-strip-trailing-space":            "{str:k=str:one  }",
	"chomp/folded-strip-trailing-space-eof":        "{str:k=str:one}",
	"chomp/literal-clip-blank-only":                "{str:k=str:}",
	"chomp/literal-clip-blank-then-eof":            "{str:k=str:one\n\ntwo}",
	"chomp/literal-clip-content-space-eof":         "{str:k=str:one  }",
	"chomp/literal-clip-crlf":                      "{str:k=str:one\ntwo\n}",
	"chomp/literal-clip-deeper-space-eof":          "{str:k=str:one\n }",
	"chomp/literal-clip-document-root":             "str:one\ntwo\n",
	"chomp/literal-clip-empty":                     "{str:j=int:1,str:k=str:}",
	"chomp/literal-clip-followed-by-key":           "{str:j=int:1,str:k=str:one\n}",
	"chomp/literal-clip-in-sequence":               "[str:one\ntwo\n]",
	"chomp/literal-clip-indent-first":              "{str:k=str:one\n two\n}",
	"chomp/literal-clip-indent-last":               "{str:k=str:one\n two\n}",
	"chomp/literal-clip-interior-blank":            "{str:k=str:one\n\ntwo\n}",
	"chomp/literal-clip-leading-tab":               "{str:k=str:\tone\ntwo\n}",
	"chomp/literal-clip-more-indented":             "{str:k=str:one\n two\nthree\n}",
	"chomp/literal-clip-no-final-break":            "{str:k=str:one}",
	"chomp/literal-clip-no-final-break-two-lines":  "{str:k=str:one\ntwo}",
	"chomp/literal-clip-plain":                     "{str:k=str:one\ntwo\n}",
	"chomp/literal-clip-trailing-blank":            "{str:k=str:one\n}",
	"chomp/literal-clip-trailing-blanks":           "{str:k=str:one\n}",
	"chomp/literal-clip-trailing-space":            "{str:k=str:one  \n}",
	"chomp/literal-clip-trailing-space-eof":        "{str:k=str:one\n}",
	"chomp/literal-keep-blank-only":                "{str:k=str:\n\n}",
	"chomp/literal-keep-blank-then-eof":            "{str:k=str:one\n\ntwo}",
	"chomp/literal-keep-content-space-eof":         "{str:k=str:one  }",
	"chomp/literal-keep-crlf":                      "{str:k=str:one\ntwo\n}",
	"chomp/literal-keep-deeper-space-eof":          "{str:k=str:one\n }",
	"chomp/literal-keep-document-root":             "str:one\ntwo\n",
	"chomp/literal-keep-empty":                     "{str:j=int:1,str:k=str:}",
	"chomp/literal-keep-followed-by-key":           "{str:j=int:1,str:k=str:one\n\n}",
	"chomp/literal-keep-in-sequence":               "[str:one\ntwo\n]",
	"chomp/literal-keep-indent-first":              "{str:k=str:one\n two\n}",
	"chomp/literal-keep-indent-last":               "{str:k=str:one\n two\n}",
	"chomp/literal-keep-interior-blank":            "{str:k=str:one\n\ntwo\n}",
	"chomp/literal-keep-leading-tab":               "{str:k=str:\tone\ntwo\n}",
	"chomp/literal-keep-more-indented":             "{str:k=str:one\n two\nthree\n}",
	"chomp/literal-keep-no-final-break":            "{str:k=str:one}",
	"chomp/literal-keep-no-final-break-two-lines":  "{str:k=str:one\ntwo}",
	"chomp/literal-keep-plain":                     "{str:k=str:one\ntwo\n}",
	"chomp/literal-keep-trailing-blank":            "{str:k=str:one\n\n}",
	"chomp/literal-keep-trailing-blanks":           "{str:k=str:one\n\n\n\n}",
	"chomp/literal-keep-trailing-space":            "{str:k=str:one  \n}",
	"chomp/literal-keep-trailing-space-eof":        "{str:k=str:one\n}",
	"chomp/literal-strip-blank-only":               "{str:k=str:}",
	"chomp/literal-strip-blank-then-eof":           "{str:k=str:one\n\ntwo}",
	"chomp/literal-strip-content-space-eof":        "{str:k=str:one  }",
	"chomp/literal-strip-crlf":                     "{str:k=str:one\ntwo}",
	"chomp/literal-strip-deeper-space-eof":         "{str:k=str:one\n }",
	"chomp/literal-strip-document-root":            "str:one\ntwo",
	"chomp/literal-strip-empty":                    "{str:j=int:1,str:k=str:}",
	"chomp/literal-strip-followed-by-key":          "{str:j=int:1,str:k=str:one}",
	"chomp/literal-strip-in-sequence":              "[str:one\ntwo]",
	"chomp/literal-strip-indent-first":             "{str:k=str:one\n two}",
	"chomp/literal-strip-indent-last":              "{str:k=str:one\n two}",
	"chomp/literal-strip-interior-blank":           "{str:k=str:one\n\ntwo}",
	"chomp/literal-strip-leading-tab":              "{str:k=str:\tone\ntwo}",
	"chomp/literal-strip-more-indented":            "{str:k=str:one\n two\nthree}",
	"chomp/literal-strip-no-final-break":           "{str:k=str:one}",
	"chomp/literal-strip-no-final-break-two-lines": "{str:k=str:one\ntwo}",
	"chomp/literal-strip-plain":                    "{str:k=str:one\ntwo}",
	"chomp/literal-strip-trailing-blank":           "{str:k=str:one}",
	"chomp/literal-strip-trailing-blanks":          "{str:k=str:one}",
	"chomp/literal-strip-trailing-space":           "{str:k=str:one  }",
	"chomp/literal-strip-trailing-space-eof":       "{str:k=str:one}",
}
