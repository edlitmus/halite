// Package yaml is halite's YAML parser: the subset of YAML 1.1 that real
// Salt state and pillar trees contain, and nothing beyond it.
//
// There is no YAML parser in the Go standard library and importing one
// would breach the dependency policy in SPEC section 4.2, so this is
// written. The subset is defined by SPEC section 10.1 and is intentionally
// strict: the parser can construct nine types and has no code path that
// constructs anything else, which is what stops a YAML document from
// becoming code execution.
//
// Mappings preserve declaration order, because state run order follows
// declaration order in the file.
package yaml

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// Error is a parse failure with a source position. Every rejection in SPEC
// section 10.1.2 produces one of these rather than a silent coercion.
type Error struct {
	Pos     value.Pos
	Msg     string
	Line    string // the offending source line, for display
	Related []Related
}

// Related is a second position that explains an error, such as the first
// occurrence of a duplicate key.
type Related struct {
	Pos value.Pos
	Msg string
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", e.Pos, e.Msg)
	for _, r := range e.Related {
		fmt.Fprintf(&b, "\n  %s: %s", r.Pos, r.Msg)
	}
	return b.String()
}

// Detail renders the error with the source line and a caret, for terminal
// output.
func (e *Error) Detail() string {
	var b strings.Builder
	b.WriteString(e.Error())
	if e.Line != "" && e.Pos.Col > 0 {
		b.WriteString("\n  " + e.Line + "\n  ")
		for i := 1; i < e.Pos.Col; i++ {
			if i-1 < len(e.Line) && e.Line[i-1] == '\t' {
				b.WriteByte('\t')
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteByte('^')
	}
	return b.String()
}

// WarnKind classifies a lint warning the parser emits while still
// producing a value.
type WarnKind int

const (
	// WarnBool11 is a scalar that resolved to a boolean only because of
	// YAML 1.1's extra spellings: yes, no, on, off, y, n. SPEC section
	// 10.1.3 requires a warning on every one of these.
	WarnBool11 WarnKind = iota
	// WarnOctalImplicit is a leading-zero integer such as 017, which
	// YAML 1.1 reads as octal and YAML 1.2 reads as decimal.
	WarnOctalImplicit
	// WarnSexagesimal is a colon-separated number such as 1:30, which
	// YAML 1.1 reads as 90 and this parser deliberately reads as a
	// string.
	WarnSexagesimal
)

// Warning is a diagnostic that does not stop parsing. `halite-node lint`
// and `halite-hub lint` report these; a normal parse collects them so a
// caller can log them.
type Warning struct {
	Kind WarnKind
	Pos  value.Pos
	Msg  string
}

func (w Warning) String() string { return fmt.Sprintf("%s: %s", w.Pos, w.Msg) }

func (p *parser) errAt(pos value.Pos, format string, args ...any) *Error {
	return &Error{Pos: pos, Msg: fmt.Sprintf(format, args...), Line: p.lineText(pos.Line)}
}

func (p *parser) err(format string, args ...any) *Error {
	return p.errAt(p.pos(), format, args...)
}

// lineText returns the source text of a 1-based line, for error display.
func (p *parser) lineText(line int) string {
	if line <= 0 {
		return ""
	}
	n := 1
	start := 0
	for i := 0; i < len(p.src); i++ {
		if n == line && p.src[i] == '\n' {
			return strings.TrimRight(string(p.src[start:i]), "\r")
		}
		if p.src[i] == '\n' {
			n++
			start = i + 1
		}
	}
	if n == line {
		return strings.TrimRight(string(p.src[start:]), "\r")
	}
	return ""
}
