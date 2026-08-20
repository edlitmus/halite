package yaml

import (
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// mergeKey is YAML's merge key. A mapping carrying it takes the keys of
// one or more aliased mappings, with its own explicit keys winning.
const mergeKey = "<<"

// nodeProps are the anchor and tag written before a node.
type nodeProps struct {
	anchor string
	tag    string
	pos    value.Pos
}

// readProps consumes any &anchor and !tag prefix at the current position.
// minIndent is the column a property continued onto a following line
// must reach; a property less indented than that belongs to something
// else, not to this node.
func (p *parser) readProps(minIndent int) (nodeProps, error) {
	var np nodeProps
	np.pos = p.pos()
	for i := 0; i < 2; i++ {
		switch p.peek() {
		case '&':
			if np.anchor != "" {
				return np, p.err("a node may carry only one anchor")
			}
			p.next()
			start := p.off
			for !p.eof() && !isFlowIndicator(p.peek()) && p.peek() != ' ' && p.peek() != '\n' {
				p.next()
			}
			np.anchor = string(p.src[start:p.off])
			if np.anchor == "" {
				return np, p.err("anchor name is empty")
			}
			p.skipToNextProp(np, minIndent)
		case '!':
			if np.tag != "" {
				return np, p.err("a node may carry only one tag")
			}
			start := p.off
			p.next()
			if p.peek() == '!' {
				p.next()
			}
			for !p.eof() && !isFlowIndicator(p.peek()) && p.peek() != ' ' && p.peek() != '\n' {
				p.next()
			}
			np.tag = string(p.src[start:p.off])
			p.skipToNextProp(np, minIndent)
		default:
			return np, nil
		}
	}
	return np, nil
}

// skipToNextProp advances past the white space after a node property,
// including a line break when another property follows it.
//
// `&a1` on one line and `!!str` on the next are properties of the same
// node, which is Spec Example 6.23 and the shape a Salt tree uses when an
// anchor would otherwise make a line too long. Stopping at the line break
// left the tag to be read as the node's content.
func (p *parser) skipToNextProp(np nodeProps, minIndent int) {
	p.skipSpaces()
	if p.eof() || p.peek() != '\n' {
		return
	}
	save := *p
	for !p.eof() && (p.peek() == '\n' || p.peek() == ' ' || p.peek() == '\t') {
		p.next()
	}
	// A node carries at most one anchor and one tag, so a line break is
	// only crossed for the kind this node has not got yet. Crossing it for
	// a second anchor would take the one belonging to the next node:
	// `top: &node\n  &key k: v` has two nodes, not one with two anchors.
	switch {
	case p.eof(), p.col < minIndent:
	case p.peek() == '&' && np.anchor == "":
		return
	case p.peek() == '!' && np.tag == "":
		return
	}
	*p = save
}

// refuseAliasProps rejects an anchor or tag written in front of an alias.
// An alias is a reference to a node, not a node of its own, so it has
// nothing for a property to attach to; `key: &b *a` names an anchor that
// would point at nothing.
func refuseAliasProps(p *parser, np nodeProps) error {
	switch {
	case np.anchor != "":
		return p.errAt(np.pos, "an alias cannot carry an anchor; &%s has nothing to name", np.anchor)
	case np.tag != "":
		return p.errAt(np.pos, "an alias cannot carry a tag; %s has nothing to apply to", np.tag)
	}
	return nil
}

func isFlowIndicator(c byte) bool {
	return c == ',' || c == '[' || c == ']' || c == '{' || c == '}'
}

// bind records an anchored node so a later alias can resolve it.
func (p *parser) bind(np nodeProps, v any) any {
	if np.anchor != "" {
		p.anchors[np.anchor] = v
	}
	return v
}

// parseBlockValue parses a node in block context.
//
// minIndent is the column below which this node's content ends.
// parentIndent is the indentation, counted in spaces, of the structure
// that owns this node; a block scalar's content must be indented further
// than that. A parentIndent of -1 means the node has no parent.
func (p *parser) parseBlockValue(minIndent, parentIndent int) (any, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer p.leave()

	if err := p.skipBlank(); err != nil {
		return nil, err
	}
	if p.eof() || p.atDocStart() || p.atDocEnd() || p.col < minIndent {
		return nil, nil
	}

	beforeProps := *p
	np, err := p.readProps(minIndent)
	if err != nil {
		return nil, err
	}
	if err := p.skipBlank(); err != nil {
		return nil, err
	}
	if p.eof() || p.col < minIndent {
		// An anchor or tag with an empty node, such as `key: !!null`.
		v, err := p.applyTag(np.tag, "", false, np.pos)
		if err != nil {
			return nil, err
		}
		return p.bind(np, v), nil
	}

	col := p.col

	switch {
	case p.peek() == '*':
		if err := refuseAliasProps(p, np); err != nil {
			return nil, err
		}
		v, err := p.parseAlias()
		if err != nil {
			return nil, err
		}
		if err := p.skipInlineTrailerIfLineEnd(); err != nil {
			return nil, err
		}
		return p.bind(np, v), nil

	case isBlockSeqEntry(p):
		v, err := p.parseBlockSeq(col)
		if err != nil {
			return nil, err
		}
		if err := p.checkCollectionTag(np, value.KindSeq); err != nil {
			return nil, err
		}
		return p.bind(np, v), nil

	case p.peek() == '[':
		v, err := p.parseFlow()
		if err != nil {
			return nil, err
		}
		if err := p.skipInlineTrailer(); err != nil {
			return nil, err
		}
		if err := p.checkCollectionTag(np, value.KindSeq); err != nil {
			return nil, err
		}
		return p.bind(np, v), nil

	case p.peek() == '{':
		v, err := p.parseFlow()
		if err != nil {
			return nil, err
		}
		if err := p.skipInlineTrailer(); err != nil {
			return nil, err
		}
		if err := p.checkCollectionTag(np, value.KindMap); err != nil {
			return nil, err
		}
		return p.bind(np, v), nil

	case p.peek() == '|' || p.peek() == '>':
		raw, err := p.parseBlockScalar(parentIndent)
		if err != nil {
			return nil, err
		}
		v, err := p.applyTag(np.tag, raw, true, np.pos)
		if err != nil {
			return nil, err
		}
		return p.bind(np, v), nil

	case p.peek() == '?' && (p.peekAt(1) == ' ' || p.peekAt(1) == '\n'):
		v, err := p.parseBlockMap(col)
		if err != nil {
			return nil, err
		}
		if err := p.checkCollectionTag(np, value.KindMap); err != nil {
			return nil, err
		}
		return p.bind(np, v), nil
	}

	if p.lineIsMappingEntry() {
		// A mapping whose first key carries its own properties starts
		// where those properties do, not where the key text does:
		// `&k1 key1: one` is a mapping at the column of the `&`, and its
		// following keys line up there. Using the key's column made the
		// mapping four characters deeper than its own second entry.
		//
		// The properties belong to that first key rather than to the
		// mapping, so the parser rewinds and lets parseBlockMap read them
		// as part of the key.
		mapCol := col
		if (np.anchor != "" || np.tag != "") && np.pos.Line == p.line {
			mapCol = np.pos.Col
			*p = beforeProps
			np = nodeProps{pos: np.pos}
		}
		v, err := p.parseBlockMap(mapCol)
		if err != nil {
			return nil, err
		}
		if err := p.checkCollectionTag(np, value.KindMap); err != nil {
			return nil, err
		}
		return p.bind(np, v), nil
	}

	// A plain scalar continued onto following lines is bounded by its
	// parent's indentation, not by the column this node happens to start
	// at. The two differ whenever the value sits on the key's own line:
	// in `plain: a` the node starts at column 8 and its continuation is
	// anything indented past the key, so passing minIndent here ended the
	// scalar at the first continuation line and left it to be read as a
	// stray, over-indented mapping entry.
	raw, quoted, err := p.parseScalar(parentIndent+1, false, notFlow)
	if err != nil {
		return nil, err
	}
	if err := p.count(); err != nil {
		return nil, err
	}
	v, err := p.applyTag(np.tag, raw, quoted, np.pos)
	if err != nil {
		return nil, err
	}
	return p.bind(np, v), nil
}

// skipInlineTrailerIfLineEnd tolerates an alias that is the whole value on
// its line without requiring a newline at end of file.
func (p *parser) skipInlineTrailerIfLineEnd() error {
	p.skipSpaces()
	if p.eof() || p.peek() == '\n' || p.peek() == '#' {
		return p.skipInlineTrailer()
	}
	return nil
}

func (p *parser) checkCollectionTag(np nodeProps, k value.Kind) error {
	switch np.tag {
	case "", "!!seq", "tag:yaml.org,2002:seq", "!!map", "tag:yaml.org,2002:map":
		if np.tag == "" {
			return nil
		}
		want := value.KindSeq
		if strings.Contains(np.tag, "map") {
			want = value.KindMap
		}
		if want != k {
			return p.errAt(np.pos, "tag %s does not match a %s", np.tag, k)
		}
		return nil
	}
	return p.errAt(np.pos, "unsupported tag %s on a %s; halite constructs only the nine types in SPEC section 10.1.1", np.tag, k)
}

func (p *parser) parseAlias() (any, error) {
	pos := p.pos()
	p.next()
	start := p.off
	for !p.eof() && !isFlowIndicator(p.peek()) && p.peek() != ' ' && p.peek() != '\n' {
		p.next()
	}
	name := string(p.src[start:p.off])
	if name == "" {
		return nil, p.errAt(pos, "alias name is empty")
	}
	v, ok := p.anchors[name]
	if !ok {
		return nil, p.errAt(pos, "alias *%s refers to an anchor that has not been defined", name)
	}
	// The copy is what bounds an expansion bomb: the node budget counts
	// every node the alias brings in, so a document that doubles itself
	// on each level stops at the budget rather than at memory exhaustion.
	n := countNodes(v)
	p.nodes += n
	if p.nodes > p.opts.MaxNodes {
		return nil, p.errAt(pos, "expanding alias *%s takes the document past the %d node budget from SPEC section 10.1.2", name, p.opts.MaxNodes)
	}
	if d := depthOf(v, 0); d > p.opts.MaxAliasDepth {
		return nil, p.errAt(pos, "alias *%s expands to depth %d, past the limit of %d", name, d, p.opts.MaxAliasDepth)
	}
	return value.Deep(v), nil
}

func countNodes(v any) int {
	switch t := v.(type) {
	case *value.Map:
		n := 1
		for _, e := range t.Entries() {
			n += 1 + countNodes(e.Val)
		}
		return n
	case []any:
		n := 1
		for _, e := range t {
			n += countNodes(e)
		}
		return n
	default:
		return 1
	}
}

func depthOf(v any, d int) int {
	if d > 200 {
		return d
	}
	switch t := v.(type) {
	case *value.Map:
		max := d
		for _, e := range t.Entries() {
			if x := depthOf(e.Val, d+1); x > max {
				max = x
			}
		}
		return max
	case []any:
		max := d
		for _, e := range t {
			if x := depthOf(e, d+1); x > max {
				max = x
			}
		}
		return max
	default:
		return d
	}
}
