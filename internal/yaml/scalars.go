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
// flowMode says which flow construct a scalar sits in, because the two
// bound an implicit key differently: inside a flow sequence a key must fit
// on one line, and inside a flow mapping it may take a line break before
// its colon. One rule for both is wrong whichever way it is written.
type flowMode int

const (
	notFlow flowMode = iota
	flowInSeq
	flowInMap
)

func (p *parser) parseScalar(minIndent int, asKey bool, flow flowMode) (string, bool, error) {
	switch p.peek() {
	case '\'':
		s, err := p.parseSingleQuoted()
		return s, true, err
	case '"':
		s, err := p.parseDoubleQuoted()
		return s, true, err
	}
	s, err := p.parsePlain(minIndent, asKey, flow)
	return s, false, err
}

// parsePlain reads an unquoted scalar, including its continuation onto
// more-indented following lines, which YAML folds with a space.
func (p *parser) parsePlain(minIndent int, asKey bool, flow flowMode) (string, error) {
	inFlow := flow != notFlow
	var lines []string

	for {
		lineStart := p.off
		start := p.off
		end := p.off
		// atBreak records that the scan stopped at a line break rather
		// than at something that ends the scalar for good. Only a break
		// can be folded into a continuation; stopping at a `:` or a flow
		// indicator means this scalar is finished.
		atBreak := false
		for !p.eof() {
			c := p.peek()
			if c == '\n' {
				atBreak = true
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
				if n == ' ' || n == '\t' || n == '\n' || n == 0 || (inFlow && isFlowIndicator(n)) {
					if flow == flowInSeq && hasContent(lines) {
						// The scalar already folded a line break with
						// something on it, so it cannot be the key of a
						// single-pair entry: inside a flow sequence such a
						// key must sit on one line, which is what keeps a
						// parser's lookahead bounded. A flow mapping is
						// the other case and allows the break, so this
						// fires only for a sequence. A blank line before
						// the scalar does not count, since the key still
						// starts on one line.
						return "", p.err("a key written inside a flow sequence must be on one line")
					}
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

		if asKey || p.eof() || !atBreak {
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

		stop := p.eof() || p.col <= minIndent || p.atDocStart() || p.atDocEnd() || p.peek() == '#'
		if inFlow {
			// Inside a flow collection a plain scalar spans lines too:
			// `{ multi\n  line, a: b}` has the key "multi line". What
			// ends it is a flow indicator rather than a block structure,
			// since there is no block structure to run into.
			stop = stop || isFlowIndicator(p.peek())
		} else {
			stop = stop || isBlockSeqEntry(p) || p.lineIsMappingEntry()
		}
		if stop {
			*p = save
			break
		}
		// A continuation that would consume nothing is not a
		// continuation. Without this a flow scalar that ends at a `:`
		// re-scanned the same empty span forever.
		if p.off == lineStart {
			*p = save
			break
		}
		for i := 1; i < blanks; i++ {
			lines = append(lines, "")
		}
	}

	return foldLines(lines), nil
}

// hasContent reports whether any line collected so far holds something
// other than white space.
func hasContent(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return true
		}
	}
	return false
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
	var lines []qline
	var cur qline
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
				cur.write("'", false)
				continue
			}
			lines = append(lines, cur)
			return foldQuoted(lines), nil
		case '\n':
			p.next()
			lines = append(lines, cur)
			cur = qline{}
			p.skipSpaces()
		default:
			// A single-quoted scalar has no escape that can produce
			// whitespace, so nothing in it is protected from folding.
			p.next()
			cur.write(string(c), false)
		}
	}
}

func (p *parser) parseDoubleQuoted() (string, error) {
	openPos := p.pos()
	p.next()
	var lines []qline
	var cur qline
	for {
		if p.eof() {
			return "", p.errAt(openPos, "unterminated double-quoted string")
		}
		c := p.peek()
		switch c {
		case '"':
			p.next()
			lines = append(lines, cur)
			return foldQuoted(lines), nil
		case '\n':
			p.next()
			lines = append(lines, cur)
			cur = qline{}
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
			str, err := p.escape(e)
			if err != nil {
				return "", err
			}
			// What an escape produced is content, even when it is a space
			// or a tab. Folding must not trim it away.
			cur.write(str, true)
		default:
			p.next()
			cur.write(string(c), false)
		}
	}
}

// qline is one line of a quoted scalar, remembering which of its bytes
// came from an escape.
//
// The distinction only matters at a line boundary: folding trims the
// whitespace around a line break, and `\t` written as an escape is a tab
// the author asked for rather than layout. Without the mask the two are
// the same byte, and an escaped tab at the end of a folded line
// disappeared.
type qline struct {
	text      strings.Builder
	protected []bool
}

func (l *qline) write(s string, protected bool) {
	l.text.WriteString(s)
	for i := 0; i < len(s); i++ {
		l.protected = append(l.protected, protected)
	}
}

func (l qline) String() string { return l.text.String() }

// trimmed returns the line with unprotected leading and trailing blanks
// removed, as folding requires.
func (l qline) trimmed(left, right bool) string {
	s := l.text.String()
	start, end := 0, len(s)
	if left {
		for start < end && isBlankByte(s[start]) && !l.protected[start] {
			start++
		}
	}
	if right {
		for end > start && isBlankByte(s[end-1]) && !l.protected[end-1] {
			end--
		}
	}
	return s[start:end]
}

// blank reports whether the line folds away to nothing, which is what
// turns a run of empty lines into line breaks.
func (l qline) blank() bool { return l.trimmed(true, true) == "" }

func isBlankByte(c byte) bool { return c == ' ' || c == '\t' }

// foldQuoted applies YAML's folding to the lines of a quoted scalar. It
// differs from a plain scalar only in that leading spaces on the last line
// and trailing spaces on the first are handled separately.
func foldQuoted(lines []qline) string {
	if len(lines) == 1 {
		return lines[0].String()
	}
	var b strings.Builder
	pendingBreaks := 0
	for i, ln := range lines {
		switch {
		case i == 0:
			b.WriteString(ln.trimmed(false, true))
		case i == len(lines)-1:
			text := ln.trimmed(true, false)
			if pendingBreaks > 0 {
				b.WriteString(strings.Repeat("\n", pendingBreaks))
			} else {
				b.WriteByte(' ')
			}
			b.WriteString(text)
		default:
			if ln.blank() {
				pendingBreaks++
				continue
			}
			if pendingBreaks > 0 {
				b.WriteString(strings.Repeat("\n", pendingBreaks))
				pendingBreaks = 0
			} else {
				b.WriteByte(' ')
			}
			b.WriteString(ln.trimmed(true, true))
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
		// `\xNN` names the code point U+00NN, not the byte NN. Emitting
		// the raw byte produced a string that is not valid UTF-8 for
		// anything above 0x7F, which the encoder then wrote out and the
		// parser refused to read back.
		return string(rune(v)), nil
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
	// leadingBlankIndent is the deepest blank line seen before any
	// content, which the detected indent has to account for.
	leadingBlankIndent := 0
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

		if blank && !detected && indent > leadingBlankIndent {
			leadingBlankIndent = indent
		}
		if !blank {
			if !detected {
				if indent <= parentIndent {
					// Nothing is indented further than the owner, so the
					// block scalar has no content: `key: |` with the next
					// line back at the mapping's own column is an empty
					// scalar, not a mistake. The line belongs to whatever
					// owns the mapping, so the parser hands it back.
					p.off = lineStart
					p.col = startCol
					raw = nil
					break
				}
				blockIndent = indent
				detected = true
				if leadingBlankIndent > blockIndent {
					// A blank line before the content may not be more
					// indented than the content itself: it would look
					// like the block started further in than it does,
					// and YAML calls that an error rather than guessing.
					return "", p.err("a blank line before a block scalar's content is indented further than the content")
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
			// A blank line's indentation is not nothing: whitespace past
			// the block's own indent is content, so `|+` over a line of
			// two spaces at indent 1 keeps one of them.
			raw = append(raw, rawLine{blank: true, indent: indent})
			continue
		}
		raw = append(raw, rawLine{text: text, indent: indent})
	}

	// A whitespace-only line indented past the block is not blank: the
	// spaces beyond the block's own indent are content. That matters at
	// the end of a scalar, where chomping counts trailing *blank* lines,
	// so `|` over "x" and a line of three spaces at indent 2 keeps the
	// leftover space and its line break.
	for i := range raw {
		if raw[i].blank && detected && raw[i].indent > blockIndent {
			raw[i].blank = false
			raw[i].text = ""
		}
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

	// extra is the whitespace a line carries past the block's own indent,
	// which is content in both styles.
	extra := func(ln rawLine) string {
		if n := ln.indent - blockIndent; n > 0 {
			return strings.Repeat(" ", n)
		}
		return ""
	}
	// moreIndented reports whether a line begins with white space past the
	// block indent. Such a line's breaks are preserved rather than folded,
	// which is the rule SPEC section 10.1.1 names and naive
	// implementations miss. A leading tab counts: the scanner stops
	// counting indentation at it, so it arrives at the head of the text.
	moreIndented := func(ln rawLine) bool {
		return ln.indent > blockIndent || strings.HasPrefix(ln.text, "\t")
	}

	var b strings.Builder
	if !folded {
		for i, ln := range raw {
			if ln.blank {
				b.WriteString(extra(ln))
				b.WriteByte('\n')
				continue
			}
			b.WriteString(extra(ln))
			b.WriteString(ln.text)
			if i < len(raw)-1 {
				b.WriteByte('\n')
			}
		}
	} else {
		// Folding, stated as it actually works: between two content lines
		// separated by k blank lines, the break becomes a space when k is
		// zero and k newlines otherwise, plus one more newline whenever
		// either of the two lines is more indented, because that break is
		// preserved rather than folded.
		blanks := 0
		started := false
		prevMore := false
		for _, ln := range raw {
			if ln.blank {
				blanks++
				continue
			}
			more := moreIndented(ln)
			switch {
			case !started:
				// Blank lines before any content are literal breaks.
				b.WriteString(strings.Repeat("\n", blanks))
				started = true
			case blanks > 0:
				n := blanks
				if more || prevMore {
					n++
				}
				b.WriteString(strings.Repeat("\n", n))
			case more || prevMore:
				b.WriteByte('\n')
			default:
				b.WriteByte(' ')
			}
			blanks = 0
			b.WriteString(extra(ln))
			b.WriteString(ln.text)
			prevMore = more
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
