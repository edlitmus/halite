// Package regexcompat is the honest limitation of SPEC section 10.4 made
// mechanical.
//
// Go's regexp package implements RE2, which guarantees linear-time
// matching and therefore has no backreferences, no lookahead, no
// lookbehind, no atomic groups, and no recursion. Python's re, which Salt
// uses, has all of them.
//
// The rule this package enforces: a pattern using a construct RE2 lacks is
// a hard error naming the construct, never a silent non-match. A silent
// non-match in file.replace is a state that reports success and changes
// nothing, which is the worst available outcome.
package regexcompat

import (
	"fmt"
	"regexp"
	"strings"
)

// Construct is one unsupported piece of a pattern.
type Construct struct {
	// Syntax is the literal text found, such as "(?=".
	Syntax string
	// Name is the human name of the feature.
	Name string
	// Offset is the byte offset in the pattern.
	Offset int
	// Workaround suggests the migration.
	Workaround string
}

func (c Construct) String() string {
	return fmt.Sprintf("%s at offset %d (%s)", c.Syntax, c.Offset, c.Name)
}

type probe struct {
	syntax     string
	name       string
	workaround string
}

var probes = []probe{
	{"(?=", "lookahead", "restructure the pattern, or match a wider span and check the remainder separately"},
	{"(?!", "negative lookahead", "restructure the pattern, or filter the matches afterwards"},
	{"(?<=", "lookbehind", "capture the leading context in a group and re-emit it in the replacement"},
	{"(?<!", "negative lookbehind", "capture the leading context in a group and filter the matches afterwards"},
	{"(?>", "atomic group", "RE2 needs no atomic group; remove it, since RE2 cannot backtrack in the first place"},
	{"(?R", "recursion", "no equivalent; the pattern must be rewritten or the work moved into a module"},
	{"(?(", "conditional group", "split the pattern into two and choose between them in the template"},
	{`\G`, "anchor to the end of the previous match", "no equivalent; iterate the matches instead"},
	{`\K`, "match reset", "capture the prefix in a group instead"},
}

// Unsupported reports every construct in a pattern that RE2 cannot
// express. It is used by the filters, by `lint`, and by the migration
// report, which is what makes the size of a migration measurable before
// it is committed to rather than discovered during it.
func Unsupported(pattern string) []Construct {
	var found []Construct
	for _, p := range probes {
		off := 0
		for {
			i := strings.Index(pattern[off:], p.syntax)
			if i < 0 {
				break
			}
			at := off + i
			if !escaped(pattern, at) {
				found = append(found, Construct{Syntax: p.syntax, Name: p.name, Offset: at, Workaround: p.workaround})
			}
			off = at + 1
		}
	}
	found = append(found, backreferences(pattern)...)
	return found
}

// backreferences finds \1 through \9 and the named forms, which RE2 has
// no way to express.
func backreferences(pattern string) []Construct {
	var found []Construct
	for i := 0; i < len(pattern)-1; i++ {
		if pattern[i] != '\\' || escaped(pattern, i) {
			continue
		}
		c := pattern[i+1]
		switch {
		case c >= '1' && c <= '9':
			found = append(found, Construct{
				Syntax: pattern[i : i+2], Name: "backreference", Offset: i,
				Workaround: "match the repeated text literally, or do the comparison outside the pattern",
			})
		case strings.HasPrefix(pattern[i:], `\k<`), strings.HasPrefix(pattern[i:], `\k'`):
			found = append(found, Construct{
				Syntax: `\k`, Name: "named backreference", Offset: i,
				Workaround: "match the repeated text literally, or do the comparison outside the pattern",
			})
		}
	}
	return found
}

// escaped reports whether the character at i is preceded by an odd number
// of backslashes, and is therefore itself escaped.
func escaped(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// Error is a refusal to compile a pattern, naming what RE2 lacks.
type Error struct {
	Pattern    string
	Constructs []Construct
	Cause      error
}

func (e *Error) Error() string {
	if len(e.Constructs) == 0 {
		return fmt.Sprintf("invalid regular expression %q: %v", e.Pattern, e.Cause)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "regular expression %q uses %d construct(s) that RE2 does not support:", e.Pattern, len(e.Constructs))
	for _, c := range e.Constructs {
		fmt.Fprintf(&b, "\n  %s: %s", c.Syntax, c.Name)
		if c.Workaround != "" {
			fmt.Fprintf(&b, "\n    %s", c.Workaround)
		}
	}
	b.WriteString("\n  See SPEC section 10.4.")
	return b.String()
}

func (e *Error) Unwrap() error { return e.Cause }

// Compile builds a pattern, reporting an unsupported construct by name
// rather than letting RE2's own message confuse the reader.
func Compile(pattern string) (*regexp.Regexp, error) {
	if cs := Unsupported(pattern); len(cs) > 0 {
		return nil, &Error{Pattern: pattern, Constructs: cs}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, &Error{Pattern: pattern, Cause: err}
	}
	return re, nil
}

// CompileWithFlags applies Python's inline flag spellings that RE2 shares,
// so a pattern carried over from a Salt tree compiles unchanged.
func CompileWithFlags(pattern string, ignoreCase, multiline, dotAll bool) (*regexp.Regexp, error) {
	var flags string
	if ignoreCase {
		flags += "i"
	}
	if multiline {
		flags += "m"
	}
	if dotAll {
		flags += "s"
	}
	if flags != "" {
		pattern = "(?" + flags + ")" + pattern
	}
	return Compile(pattern)
}
