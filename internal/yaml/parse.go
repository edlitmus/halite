package yaml

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/edlitmus/halite/internal/value"
)

// Options controls a parse. The zero value is not usable; use
// DefaultOptions and adjust.
type Options struct {
	// File names the source for diagnostics.
	File string
	// Bool11 enables YAML 1.1's extra boolean spellings (yes, no, on,
	// off, y, n). Default true, because existing Salt trees depend on
	// them. SPEC section 10.1.3 requires a warning on every one.
	Bool11 bool
	// MaxDepth bounds nesting. Default 100.
	MaxDepth int
	// MaxNodes bounds the total constructed node count, which is what
	// stops an alias-expansion bomb. Default 1,000,000.
	MaxNodes int
	// MaxAliasDepth bounds alias chains. Default 100.
	MaxAliasDepth int
	// Stream permits more than one document. An SLS file is a single
	// document; only a caller that asked for a stream sets this.
	Stream bool
	// AllowDuplicateKeys downgrades a duplicate mapping key from an error
	// to a warning. Off, because PyYAML's silent last-wins is a frequent
	// and invisible cause of a state that does nothing.
	AllowDuplicateKeys bool
}

// DefaultOptions returns the parse options SPEC section 10.1 specifies.
func DefaultOptions(file string) Options {
	return Options{
		File:          file,
		Bool11:        true,
		MaxDepth:      100,
		MaxNodes:      1_000_000,
		MaxAliasDepth: 100,
	}
}

type parser struct {
	src      []byte
	off      int
	line     int
	col      int
	opts     Options
	anchors  map[string]any
	warnings []Warning
	nodes    int
	depth    int
}

// Parse reads a single YAML document. It returns the value, any lint
// warnings, and the first error.
func Parse(src []byte, opts Options) (any, []Warning, error) {
	docs, warns, err := parseStream(src, opts)
	if err != nil {
		return nil, warns, err
	}
	if len(docs) == 0 {
		return nil, warns, nil
	}
	return docs[0], warns, nil
}

// ParseStream reads every document in a stream.
func ParseStream(src []byte, opts Options) ([]any, []Warning, error) {
	opts.Stream = true
	return parseStream(src, opts)
}

// ParseFile reads and parses a file, filling in the file name for
// diagnostics.
func ParseFile(path string, opts Options) (any, []Warning, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if opts.File == "" {
		opts.File = path
	}
	return Parse(b, opts)
}

func parseStream(src []byte, opts Options) ([]any, []Warning, error) {
	if opts.MaxDepth == 0 {
		opts.MaxDepth = 100
	}
	if opts.MaxNodes == 0 {
		opts.MaxNodes = 1_000_000
	}
	if opts.MaxAliasDepth == 0 {
		opts.MaxAliasDepth = 100
	}

	src = bytes.TrimPrefix(src, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(src) {
		return nil, nil, &Error{
			Pos: value.Pos{File: opts.File},
			Msg: "input is not valid UTF-8; halite parses UTF-8 only",
		}
	}
	src = normalizeNewlines(src)

	p := &parser{src: src, line: 1, col: 1, opts: opts, anchors: map[string]any{}}

	var docs []any
	// docClosed tracks whether a directive would be legal here: at the
	// start of a stream, or after a `...` marker closed the last document.
	docClosed := true
	for {
		if err := p.skipBlank(); err != nil {
			return nil, p.warnings, err
		}
		if p.eof() {
			break
		}
		if p.atDocEnd() {
			p.skipLine()
			docClosed = true
			continue
		}
		if err := p.skipDirectives(docClosed); err != nil {
			return nil, p.warnings, err
		}
		if p.eof() {
			break
		}
		if p.atDocStart() {
			// Consume the three dashes rather than the whole line: a
			// document may begin on the marker line itself, as in
			// `--- |` or `--- !!str x` or `--- foo`, and skipping to the
			// newline threw that node away. What followed was then
			// reparsed as a plain scalar, which is why a block scalar
			// written this way silently lost its style and its chomping.
			p.next()
			p.next()
			p.next()
			// A directives-end marker resets the anchor scope.
			p.anchors = map[string]any{}
			p.skipSpaces()

			inline := !p.eof() && p.peek() != '\n' && !p.commentStart()
			if !inline {
				if err := p.skipBlank(); err != nil {
					return nil, p.warnings, err
				}
				if p.eof() || p.atDocStart() || p.atDocEnd() {
					docs = append(docs, nil)
					docClosed = false
					continue
				}
			}
		}
		// A node on the marker line sits at whatever column the marker
		// left it at, and its parent is the document, so the minimum
		// indentation stays -1 either way.
		v, err := p.parseBlockValue(0, -1)
		if err != nil {
			return nil, p.warnings, err
		}
		docs = append(docs, v)
		docClosed = false

		if err := p.skipBlank(); err != nil {
			return nil, p.warnings, err
		}
		if p.eof() {
			break
		}
		if !p.atDocStart() && !p.atDocEnd() {
			return nil, p.warnings, p.err("unexpected content after the document; expected end of file or a --- document marker")
		}
		if !opts.Stream && !p.onlyTrailingMarkers() {
			return nil, p.warnings, p.err("this file has more than one YAML document; an SLS file must contain exactly one")
		}
	}

	if !opts.Stream && len(docs) > 1 {
		return nil, p.warnings, &Error{
			Pos: value.Pos{File: opts.File},
			Msg: fmt.Sprintf("this file has %d YAML documents; an SLS file must contain exactly one", len(docs)),
		}
	}
	return docs, p.warnings, nil
}

// skipDirectives consumes the %YAML and %TAG lines that may precede a
// document, which are stream metadata rather than content.
//
// They are recognised only here, before a document's `---`, because that
// is the only place YAML allows them: a `%` at the start of a line inside
// a document is an ordinary plain scalar, and treating it as a directive
// there would eat a value.
// isVersionNumber reports whether s is a bare `major.minor`, which is
// what a %YAML directive takes. `1.1#...` is not: a comment needs a space
// in front of it, so those characters are part of the version and the
// version is malformed.
func isVersionNumber(s string) bool {
	major, minor, ok := strings.Cut(s, ".")
	if !ok || major == "" || minor == "" {
		return false
	}
	for _, part := range [2]string{major, minor} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func (p *parser) skipDirectives(afterDocEnd bool) error {
	seen := false
	seenYAML := false
	for !p.eof() && p.col == 1 && p.peek() == '%' {
		pos := p.pos()
		start := p.off
		for !p.eof() && p.peek() != '\n' {
			p.next()
		}
		line := strings.TrimSpace(string(p.src[start:p.off]))
		if !p.eof() {
			p.next()
		}
		if !afterDocEnd {
			// A directive opens a new stream section, so a document
			// before it has to have been closed with `...`.
			return p.errAt(pos, "a directive must be preceded by a ... document end marker")
		}
		seen = true

		// A directive's parameters are separated by spaces and the line
		// ends at a comment only if a space precedes the `#`, so
		// `%YAML 1.1#...` is a malformed version rather than a comment.
		fields := strings.Fields(strings.TrimPrefix(line, "%"))
		if len(fields) == 0 {
			return p.errAt(pos, "a directive needs a name")
		}
		name, args := fields[0], fields[1:]
		for i, a := range args {
			if strings.HasPrefix(a, "#") {
				args = args[:i]
				break
			}
		}

		switch name {
		case "YAML":
			if seenYAML {
				return p.errAt(pos, "a document may carry only one %%YAML directive")
			}
			seenYAML = true
			if len(args) != 1 {
				return p.errAt(pos, "%%YAML takes exactly one version, found %d", len(args))
			}
			v := args[0]
			if !isVersionNumber(v) {
				return p.errAt(pos, "%%YAML takes a version such as 1.1, found %q", v)
			}
			if v != "1.1" && v != "1.2" {
				p.warn(WarnDirective, pos,
					"%%YAML directive names version %q; halite implements the 1.1 subset of SPEC section 10.1 and ignored it", v)
			}
		case "TAG":
			// The handle is consumed. Nothing can come of defining one,
			// since SPEC 10.1.2 admits only the tags of the nine types,
			// and a document using the handle fails there with a message
			// naming the tag.
			p.warn(WarnDirective, pos,
				"%%TAG directive %q ignored; halite admits only the tags of the nine types in SPEC section 10.1.1",
				strings.Join(args, " "))
		default:
			p.warn(WarnDirective, pos, "unknown directive %q ignored", line)
		}

		if err := p.skipBlank(); err != nil {
			return err
		}
	}
	if seen && (p.eof() || !p.atDocStart()) {
		// A directive is metadata for a document, so there has to be one.
		return p.err("a directive must be followed by a --- document marker")
	}
	return nil
}

func normalizeNewlines(src []byte) []byte {
	if !bytes.ContainsRune(src, '\r') {
		return src
	}
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(src, []byte("\r"), []byte("\n"))
}

// onlyTrailingMarkers reports whether the rest of the stream is document
// markers and whitespace, so that a file ending in "..." is still one
// document rather than two.
func (p *parser) onlyTrailingMarkers() bool {
	save := *p
	defer func() { *p = save }()
	for {
		if err := p.skipBlank(); err != nil {
			return false
		}
		if p.eof() {
			return true
		}
		if p.atDocStart() || p.atDocEnd() {
			p.skipLine()
			continue
		}
		return false
	}
}

// ---- reader primitives ----

func (p *parser) eof() bool { return p.off >= len(p.src) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.off]
}

func (p *parser) peekAt(n int) byte {
	if p.off+n >= len(p.src) {
		return 0
	}
	return p.src[p.off+n]
}

func (p *parser) next() byte {
	c := p.src[p.off]
	p.off++
	if c == '\n' {
		p.line++
		p.col = 1
	} else {
		p.col++
	}
	return c
}

func (p *parser) pos() value.Pos {
	return value.Pos{File: p.opts.File, Line: p.line, Col: p.col}
}

func (p *parser) warn(kind WarnKind, pos value.Pos, format string, args ...any) {
	p.warnings = append(p.warnings, Warning{Kind: kind, Pos: pos, Msg: fmt.Sprintf(format, args...)})
}

func (p *parser) count() error {
	p.nodes++
	if p.nodes > p.opts.MaxNodes {
		return p.err("document expands to more than %d nodes; this is the alias-expansion budget from SPEC section 10.1.2", p.opts.MaxNodes)
	}
	return nil
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > p.opts.MaxDepth {
		return p.err("nesting deeper than %d levels", p.opts.MaxDepth)
	}
	return nil
}

func (p *parser) leave() { p.depth-- }

func (p *parser) skipSpaces() {
	for !p.eof() && (p.peek() == ' ' || p.peek() == '\t') {
		p.next()
	}
}

func (p *parser) skipLine() {
	for !p.eof() && p.peek() != '\n' {
		p.next()
	}
	if !p.eof() {
		p.next()
	}
}

// atIndentation reports whether everything from the start of the current
// line to here is whitespace, which is how a tab is told apart from a tab
// used as separation inside a line.
func (p *parser) atIndentation() bool {
	for i := p.off - 1; i >= 0; i-- {
		switch p.src[i] {
		case '\n':
			return true
		case ' ', '\t':
		default:
			return false
		}
	}
	return true
}

// commentStart reports whether a '#' here begins a comment. YAML starts a
// comment only at the beginning of a line or after whitespace, which is
// what keeps the fragment in http://host/path#frag literal.
func (p *parser) commentStart() bool {
	if p.off == 0 {
		return true
	}
	switch p.src[p.off-1] {
	case ' ', '\t', '\n':
		return true
	}
	return false
}

// skipBlank advances to the next content character, crossing blank lines
// and comments.
func (p *parser) skipBlank() error {
	for !p.eof() {
		switch c := p.peek(); {
		case c == ' ' || c == '\n':
			p.next()
		case c == '\t':
			if p.atIndentation() {
				return p.err("tab character used for indentation; YAML permits only spaces")
			}
			p.next()
		case c == '#' && p.commentStart():
			p.skipLine()
		default:
			return nil
		}
	}
	return nil
}

// skipInlineTrailer consumes the spaces and any comment between a value
// and the end of its line.
func (p *parser) skipInlineTrailer() error {
	p.skipSpaces()
	if p.peek() == '#' {
		p.skipLine()
		return nil
	}
	if !p.eof() && p.peek() != '\n' {
		return p.err("unexpected %q after a value; expected end of line or a comment", string(p.peek()))
	}
	if !p.eof() {
		p.next()
	}
	return nil
}

func (p *parser) atDocStart() bool {
	return p.col == 1 && bytes.HasPrefix(p.src[p.off:], []byte("---")) && markerEnd(p, 3)
}

func (p *parser) atDocEnd() bool {
	return p.col == 1 && bytes.HasPrefix(p.src[p.off:], []byte("...")) && markerEnd(p, 3)
}

// markerEnd reports whether a document marker ends at offset n, which it
// does at end of input or at any white space. A tab separates a marker
// from what follows it just as a space does.
func markerEnd(p *parser, n int) bool {
	if p.off+n >= len(p.src) {
		return true
	}
	c := p.src[p.off+n]
	return c == ' ' || c == '\t' || c == '\n'
}

func isBlockSeqEntry(p *parser) bool {
	if p.peek() != '-' {
		return false
	}
	n := p.peekAt(1)
	// A tab separates the dash from the entry as well as a space does.
	return n == ' ' || n == '\t' || n == '\n' || n == 0
}
