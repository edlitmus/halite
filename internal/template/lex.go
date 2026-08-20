// Package template is halite's Jinja2-compatible template engine.
//
// Existing SLS trees are full of Jinja, and requiring a rewrite to Go's
// text/template would defeat the point of the project, so the engine is
// written rather than imported. It is a subset, defined by SPEC section
// 10.2, and the subset is large enough that a well-formed Salt tree
// renders unchanged.
//
// Two deliberate departures from Jinja are worth knowing before reading
// further. Undefined names are strict by default (section 10.2.6), so a
// missing pillar value is an error naming the file, line, and identifier
// rather than an empty string that produces a state which silently does
// the wrong thing. And the random filters are seeded deterministically per
// render (section 10.2.4), so a test=True run and the real run that
// follows it agree.
package template

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Pos is a position in a template source.
type Pos struct {
	File string
	Line int
	Col  int
}

func (p Pos) String() string {
	if p.File == "" {
		return fmt.Sprintf("line %d, column %d", p.Line, p.Col)
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokText
	tokVarStart // {{
	tokVarEnd   // }}
	tokTagStart // {%
	tokTagEnd   // %}
	tokName     // identifier or keyword
	tokString   // quoted literal
	tokInt      //
	tokFloat    //
	tokOp       // operator or punctuation
)

type token struct {
	kind tokenKind
	val  string
	num  any // int64 or float64 for tokInt/tokFloat
	pos  Pos
}

func (t token) String() string {
	switch t.kind {
	case tokEOF:
		return "end of template"
	case tokText:
		return "text"
	case tokVarStart:
		return "{{"
	case tokVarEnd:
		return "}}"
	case tokTagStart:
		return "{%"
	case tokTagEnd:
		return "%}"
	case tokString:
		return fmt.Sprintf("string %q", t.val)
	default:
		return fmt.Sprintf("%q", t.val)
	}
}

// Delimiters configure the markers the lexer recognises. Some SLS files
// template a file that itself contains {{ and need to move them.
type Delimiters struct {
	VarStart, VarEnd         string
	BlockStart, BlockEnd     string
	CommentStart, CommentEnd string
}

// DefaultDelimiters are Jinja's.
func DefaultDelimiters() Delimiters {
	return Delimiters{
		VarStart: "{{", VarEnd: "}}",
		BlockStart: "{%", BlockEnd: "%}",
		CommentStart: "{#", CommentEnd: "#}",
	}
}

type lexer struct {
	src    string
	file   string
	off    int
	line   int
	col    int
	delims Delimiters
	opts   lexOptions
	toks   []token
	// trimNextText records that the tag just closed asked, with `-%}` or
	// `-}}`, for the whitespace after it to go.
	trimNextText bool
}

type lexOptions struct {
	TrimBlocks   bool
	LstripBlocks bool
}

func lex(src, file string, delims Delimiters, opts lexOptions) ([]token, error) {
	l := &lexer{src: src, file: file, line: 1, col: 1, delims: delims, opts: opts}
	if err := l.run(); err != nil {
		return nil, err
	}
	return l.toks, nil
}

func (l *lexer) pos() Pos { return Pos{File: l.file, Line: l.line, Col: l.col} }

func (l *lexer) advance(n int) {
	for i := 0; i < n && l.off < len(l.src); i++ {
		if l.src[l.off] == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
		l.off++
	}
}

func (l *lexer) emit(t token) { l.toks = append(l.toks, t) }

func (l *lexer) run() error {
	for l.off < len(l.src) {
		next, kind := l.findNextDelim()
		if next < 0 {
			l.emitText(l.src[l.off:], false)
			l.advance(len(l.src) - l.off)
			break
		}

		raw := l.src[l.off:next]
		// A `-` immediately after the opening delimiter strips the
		// whitespace before the tag.
		openLen := l.delimLen(kind)
		trimBefore := next+openLen < len(l.src) && l.src[next+openLen] == '-'
		l.emitText(raw, trimBefore)
		l.advance(next - l.off)

		switch kind {
		case tokVarStart:
			if err := l.lexTag(l.delims.VarStart, l.delims.VarEnd, tokVarStart, tokVarEnd); err != nil {
				return err
			}
		case tokTagStart:
			if err := l.lexBlock(); err != nil {
				return err
			}
		default: // comment
			if err := l.lexComment(); err != nil {
				return err
			}
		}
	}
	l.emit(token{kind: tokEOF, pos: l.pos()})
	return nil
}

func (l *lexer) delimLen(kind tokenKind) int {
	switch kind {
	case tokVarStart:
		return len(l.delims.VarStart)
	case tokTagStart:
		return len(l.delims.BlockStart)
	default:
		return len(l.delims.CommentStart)
	}
}

// findNextDelim reports the offset of the next opening delimiter and which
// one it is.
func (l *lexer) findNextDelim() (int, tokenKind) {
	best := -1
	var kind tokenKind
	for _, c := range []struct {
		s string
		k tokenKind
	}{
		{l.delims.VarStart, tokVarStart},
		{l.delims.BlockStart, tokTagStart},
		{l.delims.CommentStart, 0},
	} {
		if i := strings.Index(l.src[l.off:], c.s); i >= 0 {
			if best < 0 || i < best {
				best, kind = i, c.k
			}
		}
	}
	if best < 0 {
		return -1, 0
	}
	return l.off + best, kind
}

// emitText writes a literal span, applying the whitespace controls.
//
// The reported position is where the text begins *after* trimming, not
// where the raw span began: the source map maps verbatim text line by
// line, so a position that is one line early puts every later diagnostic
// in the wrong place.
func (l *lexer) emitText(s string, trimAfter bool) {
	raw := s
	if l.trimNextText {
		s = strings.TrimLeft(s, " \t\r\n")
		l.trimNextText = false
	} else if l.opts.TrimBlocks && strings.HasPrefix(s, "\r\n") {
		s = s[2:]
	} else if l.opts.TrimBlocks && strings.HasPrefix(s, "\n") {
		s = s[1:]
	}
	skippedLines := strings.Count(raw[:len(raw)-len(s)], "\n")
	if trimAfter {
		s = strings.TrimRight(s, " \t\r\n")
	} else if l.opts.LstripBlocks {
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			if strings.TrimLeft(s[i+1:], " \t") == "" {
				s = s[:i+1]
			}
		} else if strings.TrimLeft(s, " \t") == "" && len(l.toks) == 0 {
			s = ""
		}
	}
	if s != "" {
		pos := l.pos()
		pos.Line += skippedLines
		if skippedLines > 0 {
			pos.Col = 1
		}
		l.emit(token{kind: tokText, val: s, pos: pos})
	}
}

func (l *lexer) lexComment() error {
	start := l.pos()
	l.advance(len(l.delims.CommentStart))
	if l.off < len(l.src) && l.src[l.off] == '-' {
		l.advance(1)
	}
	i := strings.Index(l.src[l.off:], l.delims.CommentEnd)
	if i < 0 {
		return &Error{Pos: start, Msg: "unterminated comment; expected " + l.delims.CommentEnd}
	}
	trimAfter := i > 0 && l.src[l.off+i-1] == '-'
	l.advance(i + len(l.delims.CommentEnd))
	l.trimNextText = trimAfter
	return nil
}

// lexBlock handles a `{% ... %}` tag, including the two that swallow their
// bodies whole: raw and verbatim.
func (l *lexer) lexBlock() error {
	if name, ok := l.peekTagName(); ok && (name == "raw" || name == "verbatim") {
		return l.lexRaw(name)
	}
	return l.lexTag(l.delims.BlockStart, l.delims.BlockEnd, tokTagStart, tokTagEnd)
}

// peekTagName reads the first word inside a `{% ... %}` without consuming.
func (l *lexer) peekTagName() (string, bool) {
	i := l.off + len(l.delims.BlockStart)
	if i < len(l.src) && l.src[i] == '-' {
		i++
	}
	for i < len(l.src) && (l.src[i] == ' ' || l.src[i] == '\t') {
		i++
	}
	start := i
	for i < len(l.src) && (isNameByte(l.src[i]) || (i > start && l.src[i] >= '0' && l.src[i] <= '9')) {
		i++
	}
	if start == i {
		return "", false
	}
	return l.src[start:i], true
}

// lexRaw copies the body of a raw block verbatim.
func (l *lexer) lexRaw(name string) error {
	start := l.pos()
	i := strings.Index(l.src[l.off:], l.delims.BlockEnd)
	if i < 0 {
		return &Error{Pos: start, Msg: "unterminated " + name + " tag"}
	}
	trimAfterOpen := i > 0 && l.src[l.off+i-1] == '-'
	l.advance(i + len(l.delims.BlockEnd))

	endTag := "end" + name
	body, consumed, trimBeforeClose, trimAfterClose, ok := findEndTag(l.src[l.off:], l.delims, endTag)
	if !ok {
		return &Error{Pos: start, Msg: "unterminated " + name + " tag; expected {% " + endTag + " %}"}
	}
	if trimAfterOpen {
		body = strings.TrimLeft(body, " \t\r\n")
	}
	if trimBeforeClose {
		body = strings.TrimRight(body, " \t\r\n")
	}
	if body != "" {
		l.emit(token{kind: tokText, val: body, pos: l.pos()})
	}
	l.advance(consumed)
	l.trimNextText = trimAfterClose
	return nil
}

// findEndTag scans for the matching `{% endX %}`, honouring nesting.
func findEndTag(src string, d Delimiters, endTag string) (body string, consumed int, trimBefore, trimAfter, ok bool) {
	depth := 0
	i := 0
	for i < len(src) {
		j := strings.Index(src[i:], d.BlockStart)
		if j < 0 {
			return "", 0, false, false, false
		}
		j += i
		// The search for the closing delimiter starts past the opening
		// one, because the two overlap: the default `{%` and `%}` share a
		// `%`, so scanning from j finds the opener's own second byte and
		// produces an end offset before the start of the tag body.
		after := j + len(d.BlockStart)
		if after > len(src) {
			return "", 0, false, false, false
		}
		k := strings.Index(src[after:], d.BlockEnd)
		if k < 0 {
			return "", 0, false, false, false
		}
		k += after
		inner := strings.TrimSpace(strings.Trim(src[j+len(d.BlockStart):k], "-"))
		word := inner
		if sp := strings.IndexAny(inner, " \t"); sp >= 0 {
			word = inner[:sp]
		}
		open := strings.TrimPrefix(endTag, "end")
		switch word {
		case open:
			depth++
		case endTag:
			if depth == 0 {
				trimBefore = src[j+len(d.BlockStart)] == '-'
				trimAfter = src[k-1] == '-'
				return src[:j], k + len(d.BlockEnd), trimBefore, trimAfter, true
			}
			depth--
		}
		i = k + len(d.BlockEnd)
	}
	return "", 0, false, false, false
}

// lexTag tokenizes the expression inside a `{{ }}` or `{% %}`.
func (l *lexer) lexTag(open, close string, openKind, closeKind tokenKind) error {
	start := l.pos()
	l.emit(token{kind: openKind, val: open, pos: start})
	l.advance(len(open))
	if l.off < len(l.src) && l.src[l.off] == '-' {
		l.advance(1)
	}

	for {
		l.skipTagSpace()
		if l.off >= len(l.src) {
			return &Error{Pos: start, Msg: "unterminated tag; expected " + close}
		}
		// The closing delimiter, with or without a whitespace-control dash.
		if l.src[l.off] == '-' && strings.HasPrefix(l.src[l.off+1:], close) {
			l.advance(1 + len(close))
			l.emit(token{kind: closeKind, val: close, pos: l.pos()})
			l.trimNextText = true
			return nil
		}
		if strings.HasPrefix(l.src[l.off:], close) {
			l.advance(len(close))
			l.emit(token{kind: closeKind, val: close, pos: l.pos()})
			return nil
		}
		if err := l.lexOne(); err != nil {
			return err
		}
	}
}

func (l *lexer) skipTagSpace() {
	for l.off < len(l.src) {
		switch l.src[l.off] {
		case ' ', '\t', '\r', '\n':
			l.advance(1)
		default:
			return
		}
	}
}

func isNameByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= utf8.RuneSelf
}

func (l *lexer) lexOne() error {
	p := l.pos()
	c := l.src[l.off]

	switch {
	case isNameByte(c):
		start := l.off
		for l.off < len(l.src) {
			ch := l.src[l.off]
			if isNameByte(ch) || (ch >= '0' && ch <= '9') {
				l.advance(1)
				continue
			}
			break
		}
		l.emit(token{kind: tokName, val: l.src[start:l.off], pos: p})
		return nil

	case c >= '0' && c <= '9':
		return l.lexNumber(p)

	case c == '\'' || c == '"':
		return l.lexString(p)
	}

	// Multi-character operators first, longest match.
	for _, op := range []string{"//", "**", "==", "!=", "<=", ">=", "and", "or"} {
		if strings.HasPrefix(l.src[l.off:], op) {
			l.advance(len(op))
			l.emit(token{kind: tokOp, val: op, pos: p})
			return nil
		}
	}
	if strings.ContainsRune("+-*/%~<>=|.,:()[]{}!", rune(c)) {
		l.advance(1)
		l.emit(token{kind: tokOp, val: string(c), pos: p})
		return nil
	}

	r, _ := utf8.DecodeRuneInString(l.src[l.off:])
	if unicode.IsSpace(r) {
		l.advance(1)
		return nil
	}
	return &Error{Pos: p, Msg: fmt.Sprintf("unexpected character %q in an expression", string(r))}
}

func (l *lexer) lexNumber(p Pos) error {
	start := l.off
	isFloat := false
	for l.off < len(l.src) {
		c := l.src[l.off]
		switch {
		case c >= '0' && c <= '9' || c == '_':
			l.advance(1)
		case c == '.':
			// A dot followed by a digit continues the number; a dot
			// followed by a name is attribute access on an integer,
			// which Jinja allows as `1.real` but SLS trees never use.
			if l.off+1 < len(l.src) && l.src[l.off+1] >= '0' && l.src[l.off+1] <= '9' && !isFloat {
				isFloat = true
				l.advance(1)
				continue
			}
			goto done
		case c == 'e' || c == 'E':
			n := byte(0)
			if l.off+1 < len(l.src) {
				n = l.src[l.off+1]
			}
			if n >= '0' && n <= '9' {
				isFloat = true
				l.advance(2)
				continue
			}
			if (n == '+' || n == '-') && l.off+2 < len(l.src) && l.src[l.off+2] >= '0' && l.src[l.off+2] <= '9' {
				isFloat = true
				l.advance(3)
				continue
			}
			goto done
		default:
			goto done
		}
	}
done:
	text := strings.ReplaceAll(l.src[start:l.off], "_", "")
	if isFloat {
		var f float64
		if _, err := fmt.Sscanf(text, "%g", &f); err != nil {
			return &Error{Pos: p, Msg: fmt.Sprintf("invalid number %q", text)}
		}
		l.emit(token{kind: tokFloat, val: text, num: f, pos: p})
		return nil
	}
	var n int64
	if _, err := fmt.Sscanf(text, "%d", &n); err != nil {
		return &Error{Pos: p, Msg: fmt.Sprintf("invalid number %q", text)}
	}
	l.emit(token{kind: tokInt, val: text, num: n, pos: p})
	return nil
}

func (l *lexer) lexString(p Pos) error {
	quote := l.src[l.off]
	l.advance(1)
	var b strings.Builder
	for {
		if l.off >= len(l.src) {
			return &Error{Pos: p, Msg: "unterminated string literal"}
		}
		c := l.src[l.off]
		if c == quote {
			l.advance(1)
			l.emit(token{kind: tokString, val: b.String(), pos: p})
			return nil
		}
		if c == '\\' && l.off+1 < len(l.src) {
			l.advance(1)
			e := l.src[l.off]
			l.advance(1)
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\':
				b.WriteByte('\\')
			case '\'':
				b.WriteByte('\'')
			case '"':
				b.WriteByte('"')
			case '0':
				b.WriteByte(0)
			case 'x', 'u', 'U':
				n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
				if l.off+n > len(l.src) {
					return &Error{Pos: p, Msg: "truncated escape in a string literal"}
				}
				var v uint32
				if _, err := fmt.Sscanf(l.src[l.off:l.off+n], "%x", &v); err != nil {
					return &Error{Pos: p, Msg: "invalid escape in a string literal"}
				}
				l.advance(n)
				if e == 'x' {
					b.WriteByte(byte(v))
				} else {
					b.WriteRune(rune(v))
				}
			default:
				b.WriteByte('\\')
				b.WriteByte(e)
			}
			continue
		}
		b.WriteByte(c)
		l.advance(1)
	}
}
