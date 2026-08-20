package yaml

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// parseScalar reads a plain, single-quoted, or double-quoted scalar and
// reports whether it was quoted. A quoted scalar is a string always; only
// a plain scalar goes through the resolution rules.
//
// asKey stops a plain scalar at the `:` that ends a mapping key. inFlow
// stops it at a flow indicator.
func (p *parser) parseScalar(minIndent int, asKey, inFlow bool) (string, bool, error) {
	switch p.peek() {
	case '\'':
		s, err := p.parseSingleQuoted()
		return s, true, err
	case '"':
		s, err := p.parseDoubleQuoted()
		return s, true, err
	}
	s, err := p.parsePlain(minIndent, asKey, inFlow)
	return s, false, err
}

// parsePlain reads an unquoted scalar, including its continuation onto
// more-indented following lines, which YAML folds with a space.
func (p *parser) parsePlain(minIndent int, asKey, inFlow bool) (string, error) {
	var lines []string

	for {
		start := p.off
		end := p.off
		for !p.eof() {
			c := p.peek()
			if c == '\n' {
				break
			}
			if c == '#' && p.off > start && (p.src[p.off-1] == ' ' || p.src[p.off-1] == '\t') {
				break
			}
			if inFlow && isFlowIndicator(c) {
				break
			}
			if c == ':' {
				n := p.peekAt(1)
				if n == ' ' || n == '\n' || n == 0 || (inFlow && isFlowIndicator(n)) {
					if asKey || inFlow {
						break
					}
					// A `: ` inside a value that is not a key is a
					// malformed mapping rather than part of a scalar.
					return "", p.err("unexpected `:` inside a plain scalar; quote the value if the colon is literal")
				}
			}
			p.next()
			if c != ' ' && c != '\t' {
				end = p.off
			}
		}
		lines = append(lines, string(p.src[start:end]))

		if asKey || inFlow || p.eof() {
			break
		}
		// A plain scalar continues on the next line only when that line
		// is more indented than the node and is not a new structure.
		save := *p
		blanks := 0
		for !p.eof() && p.peek() == '\n' {
			p.next()
			blanks++
		}
		p.skipSpaces()
		if p.eof() || p.col <= minIndent || p.atDocStart() || p.atDocEnd() ||
			p.peek() == '#' || isBlockSeqEntry(p) || p.lineIsMappingEntry() {
			*p = save
			break
		}
		for i := 1; i < blanks; i++ {
			lines = append(lines, "")
		}
	}

	return foldLines(lines), nil
}

// foldLines joins the lines of a multi-line plain scalar: a single line
// break becomes a space, and n consecutive breaks become n-1 newlines.
func foldLines(lines []string) string {
	if len(lines) == 1 {
		return strings.TrimRight(lines[0], " \t")
	}
	var b strings.Builder
	pendingBreaks := 0
	for i, ln := range lines {
		ln = strings.TrimSpace(ln)
		if i == 0 {
			b.WriteString(ln)
			continue
		}
		if ln == "" {
			pendingBreaks++
			continue
		}
		if pendingBreaks > 0 {
			b.WriteString(strings.Repeat("\n", pendingBreaks))
			pendingBreaks = 0
		} else {
			b.WriteByte(' ')
		}
		b.WriteString(ln)
	}
	return b.String()
}

func (p *parser) parseSingleQuoted() (string, error) {
	openPos := p.pos()
	p.next()
	var lines []string
	var cur strings.Builder
	for {
		if p.eof() {
			return "", p.errAt(openPos, "unterminated single-quoted string")
		}
		c := p.peek()
		switch c {
		case '\'':
			p.next()
			if p.peek() == '\'' {
				p.next()
				cur.WriteByte('\'')
				continue
			}
			lines = append(lines, cur.String())
			return foldQuoted(lines), nil
		case '\n':
			p.next()
			lines = append(lines, cur.String())
			cur.Reset()
			p.skipSpaces()
		default:
			p.next()
			cur.WriteByte(c)
		}
	}
}

func (p *parser) parseDoubleQuoted() (string, error) {
	openPos := p.pos()
	p.next()
	var lines []string
	var cur strings.Builder
	for {
		if p.eof() {
			return "", p.errAt(openPos, "unterminated double-quoted string")
		}
		c := p.peek()
		switch c {
		case '"':
			p.next()
			lines = append(lines, cur.String())
			return foldQuoted(lines), nil
		case '\n':
			p.next()
			lines = append(lines, cur.String())
			cur.Reset()
			p.skipSpaces()
		case '\\':
			p.next()
			if p.eof() {
				return "", p.errAt(openPos, "unterminated escape in a double-quoted string")
			}
			e := p.peek()
			if e == '\n' {
				// An escaped line break joins the lines with nothing
				// between them, rather than with a folded space.
				p.next()
				p.skipSpaces()
				continue
			}
			p.next()
			s, err := p.escape(e)
			if err != nil {
				return "", err
			}
			cur.WriteString(s)
		default:
			p.next()
			cur.WriteByte(c)
		}
	}
}

// foldQuoted applies YAML's folding to the lines of a quoted scalar. It
// differs from a plain scalar only in that leading spaces on the last line
// and trailing spaces on the first are handled separately.
func foldQuoted(lines []string) string {
	if len(lines) == 1 {
		return lines[0]
	}
	var b strings.Builder
	pendingBreaks := 0
	for i, ln := range lines {
		switch {
		case i == 0:
			b.WriteString(strings.TrimRight(ln, " \t"))
		case i == len(lines)-1:
			ln = strings.TrimLeft(ln, " \t")
			if pendingBreaks > 0 {
				b.WriteString(strings.Repeat("\n", pendingBreaks))
			} else {
				b.WriteByte(' ')
			}
			b.WriteString(ln)
		default:
			ln = strings.TrimSpace(ln)
			if ln == "" {
				pendingBreaks++
				continue
			}
			if pendingBreaks > 0 {
				b.WriteString(strings.Repeat("\n", pendingBreaks))
				pendingBreaks = 0
			} else {
				b.WriteByte(' ')
			}
			b.WriteString(ln)
		}
	}
	return b.String()
}

// escape resolves one double-quoted escape. The set is YAML's in full,
// which matters because file.managed contents are written this way.
func (p *parser) escape(e byte) (string, error) {
	switch e {
	case '0':
		return "\x00", nil
	case 'a':
		return "\a", nil
	case 'b':
		return "\b", nil
	case 't', '\t':
		return "\t", nil
	case 'n':
		return "\n", nil
	case 'v':
		return "\v", nil
	case 'f':
		return "\f", nil
	case 'r':
		return "\r", nil
	case 'e':
		return "\x1b", nil
	case ' ':
		return " ", nil
	case '"':
		return "\"", nil
	case '/':
		return "/", nil
	case '\\':
		return "\\", nil
	case 'N':
		return "", nil
	case '_':
		return " ", nil
	case 'L':
		return " ", nil
	case 'P':
		return " ", nil
	case 'x':
		return p.hexEscape(2)
	case 'u':
		return p.hexEscape(4)
	case 'U':
		return p.hexEscape(8)
	}
	return "", p.err("unknown escape sequence in a double-quoted string: backslash %s", string(e))
}

func (p *parser) hexEscape(n int) (string, error) {
	if p.off+n > len(p.src) {
		return "", p.err("truncated hexadecimal escape")
	}
	digits := string(p.src[p.off : p.off+n])
	for i := 0; i < n; i++ {
		p.next()
	}
	v, err := strconv.ParseUint(digits, 16, 32)
	if err != nil {
		return "", p.err("invalid hexadecimal escape %q", digits)
	}
	if n == 2 {
		return string([]byte{byte(v)}), nil
	}
	if v > utf8.MaxRune {
		return "", p.err("escape %q is not a Unicode code point", digits)
	}
	return string(rune(v)), nil
}

// parseBlockScalar reads a `|` literal or `>` folded block scalar with its
// chomping and indentation indicators.
//
// The folded form carries the rule naive implementations get wrong: a line
// indented further than the block's own indentation is "more indented",
// and its line breaks are preserved rather than folded. Salt trees rely on
// this whenever a file.managed `contents` block holds indented
// configuration.
func (p *parser) parseBlockScalar(parentIndent int) (string, error) {
	style := p.next() // '|' or '>'
	folded := style == '>'

	chomp := byte('c') // clip
	explicitIndent := 0
	for i := 0; i < 2; i++ {
		c := p.peek()
		switch {
		case c == '-' || c == '+':
			if chomp != 'c' {
				return "", p.err("a block scalar may carry only one chomping indicator")
			}
			chomp = c
			p.next()
		case c >= '1' && c <= '9':
			if explicitIndent != 0 {
				return "", p.err("a block scalar may carry only one indentation indicator")
			}
			explicitIndent = int(c - '0')
			p.next()
		default:
			i = 2
		}
	}
	p.skipSpaces()
	if p.peek() == '#' {
		p.skipLine()
	} else if p.peek() == '\n' {
		p.next()
	} else if !p.eof() {
		return "", p.err("unexpected %q after a block scalar header", string(p.peek()))
	}

	type rawLine struct {
		text   string
		indent int
		blank  bool
	}
	var raw []rawLine

	// The indentation indicator counts from the parent node's own
	// indentation. Without one, the block takes the indentation of its
	// first non-empty line.
	//
	// detected is a separate flag rather than a zero sentinel on
	// blockIndent, because zero is a legitimate detected indent: a block
	// scalar at the top of a document has a parent indent of -1, so its
	// content may begin in column 0. Reusing zero as "not yet detected"
	// let the detection run a second time on a later, deeper line, which
	// raised the indent after shallower lines had already been accepted
	// below it and left the render loop subtracting its way negative.
	blockIndent := 0
	detected := false
	if explicitIndent > 0 {
		blockIndent = max(parentIndent, 0) + explicitIndent
		detected = true
	}

	for !p.eof() {
		lineStart := p.off
		startCol := p.col
		indent := 0
		for !p.eof() && p.peek() == ' ' {
			p.next()
			indent++
		}
		blank := p.eof() || p.peek() == '\n'

		if !blank {
			if !detected {
				blockIndent = indent
				detected = true
				if blockIndent <= parentIndent {
					return "", p.err("a block scalar's content must be indented further than the key that owns it")
				}
			}
			if indent < blockIndent {
				p.off = lineStart
				p.col = startCol
				break
			}
		}

		start := p.off
		for !p.eof() && p.peek() != '\n' {
			p.next()
		}
		text := string(p.src[start:p.off])
		if !p.eof() {
			p.next()
		}
		if blank {
			raw = append(raw, rawLine{blank: true})
			continue
		}
		raw = append(raw, rawLine{text: text, indent: indent})
	}

	lastContent := -1
	for i, ln := range raw {
		if !ln.blank {
			lastContent = i
		}
	}
	if lastContent < 0 {
		if chomp == '+' {
			return strings.Repeat("\n", len(raw)), nil
		}
		return "", nil
	}
	trailing := len(raw) - 1 - lastContent
	raw = raw[:lastContent+1]

	var b strings.Builder
	if !folded {
		for i, ln := range raw {
			if ln.blank {
				b.WriteByte('\n')
				continue
			}
			b.WriteString(strings.Repeat(" ", ln.indent-blockIndent))
			b.WriteString(ln.text)
			if i < len(raw)-1 {
				b.WriteByte('\n')
			}
		}
	} else {
		prevMoreIndented := false
		for i, ln := range raw {
			if ln.blank {
				b.WriteByte('\n')
				prevMoreIndented = false
				continue
			}
			moreIndented := ln.indent > blockIndent
			if i > 0 {
				switch {
				case raw[i-1].blank:
					// The line break was already written.
				case moreIndented || prevMoreIndented:
					b.WriteByte('\n')
				default:
					b.WriteByte(' ')
				}
			}
			b.WriteString(strings.Repeat(" ", ln.indent-blockIndent))
			b.WriteString(ln.text)
			prevMoreIndented = moreIndented
		}
	}

	switch chomp {
	case '-':
		return b.String(), nil
	case '+':
		return b.String() + "\n" + strings.Repeat("\n", trailing), nil
	default:
		return b.String() + "\n", nil
	}
}
