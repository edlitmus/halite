package yaml

import (
	"github.com/edlitmus/halite/internal/value"
)

// lineIsMappingEntry looks ahead, without consuming, for the ": " that
// makes the current line a mapping entry rather than a plain scalar. It
// respects quoting and flow nesting so that `foo: [a, b]` is an entry and
// `http://host` is not.
func (p *parser) lineIsMappingEntry() bool {
	i := p.off
	flow := 0
	for i < len(p.src) {
		c := p.src[i]
		switch c {
		case '\n':
			return false
		case '\'', '"':
			if !p.quoteOpensAToken(i) {
				// A quote in the middle of a token is an ordinary
				// character: `a"b: 1` is a mapping entry whose key is
				// `a"b`, which is what PyYAML makes of it too. Treating
				// every quote as an opener made an unpaired one swallow
				// the rest of the line and hide the colon.
				i++
				continue
			}
			end, ok := p.scanQuoted(i)
			if !ok {
				return false
			}
			i = end
		case '[', '{':
			flow++
			i++
		case ']', '}':
			// A closing bracket with no opener is an ordinary character
			// in a plain scalar, not flow nesting. Letting the counter go
			// negative meant the `:` after it never matched the `flow ==
			// 0` test, so `0]: null` was not seen as a mapping entry.
			if flow > 0 {
				flow--
			}
			i++
		case '#':
			if i > p.off && (p.src[i-1] == ' ' || p.src[i-1] == '\t') {
				return false
			}
			i++
		case ':':
			if flow == 0 && (i+1 >= len(p.src) || p.src[i+1] == ' ' || p.src[i+1] == '\t' || p.src[i+1] == '\n') {
				return true
			}
			i++
		default:
			i++
		}
	}
	return false
}

// quoteOpensAToken reports whether the quote at i begins a quoted scalar
// rather than sitting inside a plain one. A quoted scalar can only start
// where a token can start: at the head of the line, or after a flow
// punctuation character.
func (p *parser) quoteOpensAToken(i int) bool {
	for j := i - 1; j >= p.off; j-- {
		switch p.src[j] {
		case ' ', '\t':
			continue
		case '[', '{', ',', ':', '-', '?':
			return true
		default:
			return false
		}
	}
	return true
}

// scanQuoted returns the offset just past the quoted scalar starting at i,
// and whether it was closed before the end of the line.
func (p *parser) scanQuoted(i int) (int, bool) {
	quote := p.src[i]
	j := i + 1
	for j < len(p.src) && p.src[j] != '\n' {
		if quote == '"' && p.src[j] == '\\' {
			j += 2
			continue
		}
		if p.src[j] == quote {
			// Inside a single-quoted scalar, a doubled quote is an
			// escaped quote rather than the close.
			if quote == '\'' && j+1 < len(p.src) && p.src[j+1] == '\'' {
				j += 2
				continue
			}
			return j + 1, true
		}
		j++
	}
	return j, false
}

// parseBlockMap reads a block mapping whose keys sit at column indent.
func (p *parser) parseBlockMap(indent int) (*value.Map, error) {
	if err := p.count(); err != nil {
		return nil, err
	}
	m := value.NewMap(8)
	m.Pos = p.pos()

	var merges []mergeRecord

	for {
		if err := p.skipBlank(); err != nil {
			return nil, err
		}
		if p.eof() || p.atDocStart() || p.atDocEnd() || p.col < indent {
			break
		}
		if p.col > indent {
			return nil, p.err("unexpected indentation; this mapping's keys start at column %d", indent)
		}
		if isBlockSeqEntry(p) {
			break
		}

		keyPos := p.pos()
		var key any
		var err error

		// noValue marks an explicit key with no `: ` line of its own, whose
		// value is null. That is how a set is written, and how a mapping
		// with a missing value is written, so refusing it rejected valid
		// YAML rather than catching a mistake.
		noValue := false

		if p.peek() == '?' && (p.peekAt(1) == ' ' || p.peekAt(1) == '\n') {
			p.next()
			p.skipSpaces()
			key, err = p.parseExplicitKey(indent)
			if err != nil {
				return nil, err
			}
			if err := p.skipBlank(); err != nil {
				return nil, err
			}
			if p.col == indent && p.peek() == ':' &&
				(p.peekAt(1) == ' ' || p.peekAt(1) == '\t' || p.peekAt(1) == '\n' || p.peekAt(1) == 0) {
				p.next()
			} else {
				noValue = true
			}
		} else {
			// A key carries anchors and tags like any other node:
			// `&anchor key: 1` anchors the key, and `!!str 1: x` keeps it
			// a string. Reading them here rather than letting them fall
			// into the scalar is what stops the key from coming out as
			// the literal text "&anchor key".
			np, err := p.readProps(indent)
			if err != nil {
				return nil, err
			}
			if np.anchor != "" || np.tag != "" {
				keyPos = np.pos
			}

			if p.peek() == '*' {
				key, err = p.parseAlias()
				if err != nil {
					return nil, err
				}
				p.skipSpaces()
			} else {
				raw, quoted, err := p.parseScalar(indent, true, notFlow)
				if err != nil {
					return nil, err
				}
				if quoted {
					key = raw
				} else {
					key = p.resolvePlain(raw, keyPos)
				}
				if np.tag != "" {
					key, err = p.applyTag(np.tag, raw, quoted, np.pos)
					if err != nil {
						return nil, err
					}
				}
			}
			// `"key" : value` and `key\t: value` are ordinary YAML: white
			// space is allowed between a key and its colon, and a quoted
			// key leaves the parser sitting on that space rather than on
			// the colon.
			p.skipSpaces()
			if p.peek() != ':' {
				return nil, p.err("expected `:` after the mapping key %q", value.KeyString(key))
			}
			p.next()
			p.bind(np, key)
		}

		if !noValue && !p.eof() && p.peek() != ' ' && p.peek() != '\t' && p.peek() != '\n' {
			return nil, p.err("a `:` that ends a mapping key must be followed by a space or a line break")
		}

		valPos := p.pos()
		var val any
		if !noValue {
			val, err = p.parseMapValue(indent)
			if err != nil {
				return nil, err
			}
		}

		if ks, ok := key.(string); ok && ks == mergeKey {
			srcs, err := mergeSources(p, val, valPos)
			if err != nil {
				return nil, err
			}
			merges = append(merges, mergeRecord{at: m.Len(), src: srcs, pos: valPos})
			continue
		}

		if prev, ok := m.Entry(key); ok {
			e := p.errAt(keyPos, "duplicate mapping key %q", value.KeyString(key))
			e.Related = []Related{{Pos: prev.KeyPos, Msg: "first defined here"}}
			if !p.opts.AllowDuplicateKeys {
				return nil, e
			}
			p.warnings = append(p.warnings, Warning{Kind: WarnDuplicateKey, Pos: keyPos, Msg: e.Msg})
		}
		if err := p.count(); err != nil {
			return nil, err
		}
		m.SetAt(key, val, keyPos, valPos)
	}

	if len(merges) == 0 {
		return m, nil
	}
	return applyMerges(m, merges)
}

// applyMerges rebuilds a mapping with merged keys inserted at the position
// the `<<` appeared, while keeping YAML's precedence rule: a key written
// explicitly in this mapping always beats the same key arriving through a
// merge, wherever the two appear.
// mergeRecord remembers one `<<` key: where it appeared in the mapping and
// which mappings it names.
type mergeRecord struct {
	at  int
	src []*value.Map
	pos value.Pos
}

func applyMerges(m *value.Map, merges []mergeRecord) (*value.Map, error) {
	explicit := make(map[string]bool, m.Len())
	for _, e := range m.Entries() {
		explicit[value.CanonKey(e.Key)] = true
	}

	out := value.NewMap(m.Len())
	out.Pos = m.Pos
	entries := m.Entries()
	mi := 0
	for i := 0; i <= len(entries); i++ {
		for mi < len(merges) && merges[mi].at == i {
			for _, src := range merges[mi].src {
				for _, e := range src.Entries() {
					ck := value.CanonKey(e.Key)
					if explicit[ck] || out.Has(e.Key) {
						continue
					}
					out.SetAt(e.Key, value.Deep(e.Val), e.KeyPos, e.ValPos)
				}
			}
			mi++
		}
		if i < len(entries) {
			e := entries[i]
			out.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
		}
	}
	return out, nil
}

// mergeSources resolves the value of a `<<` key into the mappings it
// merges. YAML gives an earlier entry in a merge sequence priority over a
// later one.
func mergeSources(p *parser, v any, pos value.Pos) ([]*value.Map, error) {
	switch t := v.(type) {
	case *value.Map:
		return []*value.Map{t}, nil
	case []any:
		var out []*value.Map
		for _, item := range t {
			m, ok := item.(*value.Map)
			if !ok {
				return nil, p.errAt(pos, "a merge key sequence may contain only mappings, found %s", value.TypeName(item))
			}
			out = append(out, m)
		}
		return out, nil
	}
	return nil, p.errAt(pos, "a merge key must name a mapping or a sequence of mappings, found %s", value.TypeName(v))
}

// parseExplicitKey reads the node after `? `. SPEC section 10.1.2 rejects
// a mapping or a sequence used as a key, so only a scalar is accepted.
func (p *parser) parseExplicitKey(indent int) (any, error) {
	pos := p.pos()
	if p.peek() == '[' || p.peek() == '{' || isBlockSeqEntry(p) {
		return nil, p.errAt(pos, "a mapping or sequence cannot be used as a key")
	}
	// A block scalar is a legal explicit key, and a common one: `? |`
	// followed by indented lines.
	if p.peek() == '|' || p.peek() == '>' {
		return p.parseBlockScalar(indent - 1)
	}
	// The key may begin on the lines below the `?` rather than beside it.
	if p.eof() || p.peek() == '\n' || p.commentStart() {
		if err := p.skipBlank(); err != nil {
			return nil, err
		}
		if p.eof() || p.col <= indent {
			return nil, nil
		}
		v, err := p.parseBlockValue(p.col, indent)
		if err != nil {
			return nil, err
		}
		switch v.(type) {
		case *value.Map, []any:
			return nil, p.errAt(pos, "a mapping or sequence cannot be used as a key")
		}
		return v, nil
	}
	raw, quoted, err := p.parseScalar(indent, false, notFlow)
	if err != nil {
		return nil, err
	}
	if quoted {
		return raw, nil
	}
	return p.resolvePlain(raw, pos), nil
}

// parseMapValue reads the value that follows a `key:`, whether it sits on
// the same line or on the lines below.
func (p *parser) parseMapValue(keyIndent int) (any, error) {
	p.skipSpaces()

	// Value on the same line. Its content is bounded by the key's own
	// indentation, not by the column the value happens to start at: an
	// anchor written as `key: &name` puts the node it names on the
	// following, less-indented lines.
	if !p.eof() && p.peek() != '\n' && p.peek() != '#' {
		if p.peek() == '|' || p.peek() == '>' {
			return p.parseBlockScalar(keyIndent - 1)
		}
		return p.parseBlockValue(keyIndent+1, keyIndent-1)
	}
	if p.peek() == '#' {
		p.skipLine()
	} else if !p.eof() {
		p.next()
	}

	// Value on the following lines.
	if err := p.skipBlank(); err != nil {
		return nil, err
	}
	if p.eof() || p.atDocStart() || p.atDocEnd() {
		return nil, nil
	}
	switch {
	case p.col > keyIndent:
		return p.parseBlockValue(p.col, keyIndent-1)
	case p.col == keyIndent && isBlockSeqEntry(p):
		// A block sequence may sit at the same indentation as the key
		// that owns it. Salt trees are written both ways, so both are
		// accepted.
		return p.parseBlockSeq(p.col)
	}
	return nil, nil
}

// parseBlockSeq reads a block sequence whose `-` indicators sit at column
// indent.
func (p *parser) parseBlockSeq(indent int) ([]any, error) {
	if err := p.count(); err != nil {
		return nil, err
	}
	items := []any{}
	for {
		if err := p.skipBlank(); err != nil {
			return nil, err
		}
		if p.eof() || p.atDocStart() || p.atDocEnd() || p.col != indent || !isBlockSeqEntry(p) {
			break
		}
		p.next() // the '-'

		p.skipSpaces()
		if p.eof() || p.peek() == '\n' || p.peek() == '#' {
			if p.peek() == '#' {
				p.skipLine()
			} else if !p.eof() {
				p.next()
			}
			if err := p.skipBlank(); err != nil {
				return nil, err
			}
			if !p.eof() && p.col > indent && !p.atDocStart() && !p.atDocEnd() {
				v, err := p.parseBlockValue(p.col, indent-1)
				if err != nil {
					return nil, err
				}
				items = append(items, v)
				continue
			}
			items = append(items, nil)
			continue
		}

		v, err := p.parseBlockValue(p.col, indent-1)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, nil
}
