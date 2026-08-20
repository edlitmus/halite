package yaml

import (
	"github.com/edlitmus/halite/internal/value"
)

// parseFlow reads a flow collection: `[1, 2, 3]` or `{a: 1, b: 2}`,
// nested to any depth. Flow collections appear throughout Salt trees in
// requisite lists and in inline pillar data.
func (p *parser) parseFlow() (any, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer p.leave()

	switch p.peek() {
	case '[':
		return p.parseFlowSeq()
	case '{':
		return p.parseFlowMap()
	}
	return nil, p.err("expected a flow collection")
}

func (p *parser) parseFlowSeq() ([]any, error) {
	openPos := p.pos()
	p.next()
	if err := p.count(); err != nil {
		return nil, err
	}
	items := []any{}
	for {
		if err := p.skipFlowBlank(); err != nil {
			return nil, err
		}
		if p.eof() {
			return nil, p.errAt(openPos, "unterminated flow sequence; expected `]`")
		}
		if p.peek() == ']' {
			p.next()
			return items, nil
		}
		v, err := p.parseFlowNode()
		if err != nil {
			return nil, err
		}
		items = append(items, v)

		if err := p.skipFlowBlank(); err != nil {
			return nil, err
		}
		switch p.peek() {
		case ',':
			p.next()
		case ']':
			p.next()
			return items, nil
		default:
			if p.eof() {
				return nil, p.errAt(openPos, "unterminated flow sequence; expected `]`")
			}
			return nil, p.err("expected `,` or `]` in a flow sequence, found %q", string(p.peek()))
		}
	}
}

func (p *parser) parseFlowMap() (*value.Map, error) {
	openPos := p.pos()
	p.next()
	if err := p.count(); err != nil {
		return nil, err
	}
	m := value.NewMap(4)
	m.Pos = openPos
	var merges []mergeRecord

	for {
		if err := p.skipFlowBlank(); err != nil {
			return nil, err
		}
		if p.eof() {
			return nil, p.errAt(openPos, "unterminated flow mapping; expected `}`")
		}
		if p.peek() == '}' {
			p.next()
			break
		}

		keyPos := p.pos()
		if p.peek() == '[' || p.peek() == '{' {
			return nil, p.errAt(keyPos, "a mapping or sequence cannot be used as a key")
		}
		raw, quoted, err := p.parseScalar(0, true, true)
		if err != nil {
			return nil, err
		}
		var key any
		if quoted {
			key = raw
		} else {
			key = p.resolvePlain(raw, keyPos)
		}

		if err := p.skipFlowBlank(); err != nil {
			return nil, err
		}
		var val any
		var valPos value.Pos
		if p.peek() == ':' {
			p.next()
			if err := p.skipFlowBlank(); err != nil {
				return nil, err
			}
			valPos = p.pos()
			if p.peek() == ',' || p.peek() == '}' {
				val = nil
			} else {
				val, err = p.parseFlowNode()
				if err != nil {
					return nil, err
				}
			}
		} else {
			// `{a, b}` is a set in YAML; halite has no set type, so the
			// keys take a null value, which is what PyYAML's dict-like
			// loading produces for Salt.
			valPos = keyPos
		}

		if ks, ok := key.(string); ok && ks == mergeKey {
			srcs, err := mergeSources(p, val, valPos)
			if err != nil {
				return nil, err
			}
			merges = append(merges, mergeRecord{at: m.Len(), src: srcs, pos: valPos})
		} else {
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

		if err := p.skipFlowBlank(); err != nil {
			return nil, err
		}
		switch p.peek() {
		case ',':
			p.next()
		case '}':
			p.next()
			if len(merges) == 0 {
				return m, nil
			}
			return applyMerges(m, merges)
		default:
			if p.eof() {
				return nil, p.errAt(openPos, "unterminated flow mapping; expected `}`")
			}
			return nil, p.err("expected `,` or `}` in a flow mapping, found %q", string(p.peek()))
		}
	}
	if len(merges) == 0 {
		return m, nil
	}
	return applyMerges(m, merges)
}

// parseFlowNode reads one entry inside a flow collection.
func (p *parser) parseFlowNode() (any, error) {
	np, err := p.readProps()
	if err != nil {
		return nil, err
	}
	if err := p.skipFlowBlank(); err != nil {
		return nil, err
	}
	switch p.peek() {
	case '*':
		v, err := p.parseAlias()
		if err != nil {
			return nil, err
		}
		return p.bind(np, v), nil
	case '[', '{':
		v, err := p.parseFlow()
		if err != nil {
			return nil, err
		}
		k := value.KindSeq
		if _, ok := v.(*value.Map); ok {
			k = value.KindMap
		}
		if err := p.checkCollectionTag(np, k); err != nil {
			return nil, err
		}
		return p.bind(np, v), nil
	}

	pos := p.pos()
	raw, quoted, err := p.parseScalar(0, false, true)
	if err != nil {
		return nil, err
	}
	if err := p.count(); err != nil {
		return nil, err
	}

	// `{a: 1, b: 2}` nested as `[{a: 1}]` reaches here only for the
	// scalar; a `:` following it means a single-pair mapping written
	// without braces, which YAML permits inside a flow sequence.
	if p.peek() == ':' && (p.peekAt(1) == ' ' || isFlowIndicator(p.peekAt(1)) || p.peekAt(1) == '\n') {
		p.next()
		if err := p.skipFlowBlank(); err != nil {
			return nil, err
		}
		m := value.NewMap(1)
		m.Pos = pos
		var key any = raw
		if !quoted {
			key = p.resolvePlain(raw, pos)
		}
		var val any
		if p.peek() != ',' && p.peek() != ']' && p.peek() != '}' {
			val, err = p.parseFlowNode()
			if err != nil {
				return nil, err
			}
		}
		m.SetAt(key, val, pos, p.pos())
		return p.bind(np, m), nil
	}

	v, err := p.applyTag(np.tag, raw, quoted, pos)
	if err != nil {
		return nil, err
	}
	return p.bind(np, v), nil
}

// skipFlowBlank crosses whitespace, line breaks, and comments inside a
// flow collection, where a line break carries no structural meaning.
func (p *parser) skipFlowBlank() error {
	for !p.eof() {
		switch c := p.peek(); {
		case c == ' ' || c == '\n':
			p.next()
		case c == '\t':
			if p.atIndentation() {
				return p.err("tab character used for indentation; YAML permits only spaces")
			}
			p.next()
		case c == '#':
			p.skipLine()
		default:
			return nil
		}
	}
	return nil
}
