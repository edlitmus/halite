package template

import (
	"strings"
)

type parser struct {
	toks []token
	i    int
	file string
	opts Options
}

// parseTemplate turns a token stream into a body of statements.
func parseTemplate(toks []token, file string, opts Options) ([]Node, error) {
	p := &parser{toks: toks, file: file, opts: opts}
	body, end, err := p.parseBody(nil)
	if err != nil {
		return nil, err
	}
	if end != "" {
		return nil, errorf(p.peek().pos, "unexpected {%% %s %%}", end)
	}
	return body, nil
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }
func (p *parser) atEOF() bool { return p.toks[p.i].kind == tokEOF }
func (p *parser) pos() Pos    { return p.toks[p.i].pos }

func (p *parser) accept(kind tokenKind, val string) bool {
	t := p.peek()
	if t.kind == kind && (val == "" || t.val == val) {
		p.i++
		return true
	}
	return false
}

func (p *parser) expect(kind tokenKind, val string) (token, error) {
	t := p.peek()
	if t.kind != kind || (val != "" && t.val != val) {
		want := val
		if want == "" {
			want = tokenKindName(kind)
		}
		return t, errorf(t.pos, "expected %s, found %s", want, t)
	}
	p.i++
	return t, nil
}

func tokenKindName(k tokenKind) string {
	switch k {
	case tokName:
		return "a name"
	case tokString:
		return "a string"
	case tokTagEnd:
		return "%}"
	case tokVarEnd:
		return "}}"
	default:
		return "a token"
	}
}

// atTagNamed reports whether the cursor sits on `{% name %}` for any of the
// given names, without consuming.
func (p *parser) atTagNamed(names ...string) (string, bool) {
	if p.peek().kind != tokTagStart {
		return "", false
	}
	nt := p.toks[p.i+1]
	if nt.kind != tokName {
		return "", false
	}
	for _, n := range names {
		if nt.val == n {
			return n, true
		}
	}
	return "", false
}

// parseBody reads statements until one of the given end tags, which it
// leaves unconsumed and names in its return.
func (p *parser) parseBody(endTags []string) ([]Node, string, error) {
	var body []Node
	for {
		if p.atEOF() {
			if len(endTags) > 0 {
				return nil, "", errorf(p.pos(), "unexpected end of template; expected {%% %s %%}", strings.Join(endTags, " %} or {% "))
			}
			return body, "", nil
		}
		if name, ok := p.atTagNamed(endTags...); ok {
			return body, name, nil
		}

		t := p.peek()
		switch t.kind {
		case tokText:
			p.i++
			body = append(body, &TextNode{baseNode{t.pos}, t.val})
		case tokVarStart:
			p.i++
			e, err := p.parseExpr()
			if err != nil {
				return nil, "", err
			}
			if _, err := p.expect(tokVarEnd, ""); err != nil {
				return nil, "", err
			}
			body = append(body, &OutputNode{baseNode{t.pos}, e})
		case tokTagStart:
			n, err := p.parseStatement()
			if err != nil {
				return nil, "", err
			}
			if n != nil {
				body = append(body, n)
			}
		default:
			return nil, "", errorf(t.pos, "unexpected %s", t)
		}
	}
}

// endTag consumes the `{% name %}` the cursor is sitting on.
func (p *parser) endTag() error {
	if _, err := p.expect(tokTagStart, ""); err != nil {
		return err
	}
	p.next() // the name
	_, err := p.expect(tokTagEnd, "")
	return err
}

func (p *parser) parseStatement() (Node, error) {
	start := p.peek().pos
	p.i++ // {%
	nameTok, err := p.expect(tokName, "")
	if err != nil {
		return nil, err
	}

	switch nameTok.val {
	case "if":
		return p.parseIf(start)
	case "for":
		return p.parseFor(start)
	case "set":
		return p.parseSet(start)
	case "macro":
		return p.parseMacro(start)
	case "call":
		return p.parseCall(start)
	case "include":
		return p.parseInclude(start)
	case "import":
		return p.parseImport(start)
	case "from":
		return p.parseFromImport(start)
	case "extends":
		return p.parseExtends(start)
	case "block":
		return p.parseBlock(start)
	case "filter":
		return p.parseFilter(start)
	case "do":
		return p.parseDo(start)
	case "with":
		return p.parseWith(start)
	case "autoescape":
		return p.parseAutoescape(start)
	case "trans", "pluralize", "load":
		return nil, errorf(start, "the %s tag is not supported; SPEC section 10.2.2", nameTok.val)
	}
	return nil, errorf(start, "unknown tag %q", nameTok.val)
}

func (p *parser) parseIf(start Pos) (Node, error) {
	n := &IfNode{baseNode: baseNode{start}}
	for {
		cond, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokTagEnd, ""); err != nil {
			return nil, err
		}
		body, end, err := p.parseBody([]string{"elif", "else", "endif"})
		if err != nil {
			return nil, err
		}
		n.Conds = append(n.Conds, cond)
		n.Blocks = append(n.Blocks, body)

		switch end {
		case "elif":
			p.i += 2 // {% elif
			continue
		case "else":
			p.i += 2
			if _, err := p.expect(tokTagEnd, ""); err != nil {
				return nil, err
			}
			elseBody, _, err := p.parseBody([]string{"endif"})
			if err != nil {
				return nil, err
			}
			n.Else = elseBody
			return n, p.endTag()
		default:
			return n, p.endTag()
		}
	}
}

func (p *parser) parseFor(start Pos) (Node, error) {
	n := &ForNode{baseNode: baseNode{start}}
	var err error
	for {
		t, terr := p.expect(tokName, "")
		if terr != nil {
			return nil, terr
		}
		n.Targets = append(n.Targets, t.val)
		if !p.accept(tokOp, ",") {
			break
		}
	}
	if _, err := p.expect(tokName, "in"); err != nil {
		return nil, err
	}
	// The iterable is parsed without the conditional-expression rule, so
	// that the `if` in `{% for x in xs if x %}` is the loop filter rather
	// than an inline conditional.
	n.Iter, err = p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind == tokName && p.peek().val == "if" {
		p.i++
		n.Cond, err = p.parseOr()
		if err != nil {
			return nil, err
		}
	}
	if p.peek().kind == tokName && p.peek().val == "recursive" {
		p.i++
		n.Recursive = true
	}
	if _, err := p.expect(tokTagEnd, ""); err != nil {
		return nil, err
	}

	body, end, err := p.parseBody([]string{"else", "endfor"})
	if err != nil {
		return nil, err
	}
	n.Body = body
	if end == "else" {
		p.i += 2
		if _, err := p.expect(tokTagEnd, ""); err != nil {
			return nil, err
		}
		elseBody, _, err := p.parseBody([]string{"endfor"})
		if err != nil {
			return nil, err
		}
		n.Else = elseBody
	}
	return n, p.endTag()
}

func (p *parser) parseSet(start Pos) (Node, error) {
	n := &SetNode{baseNode: baseNode{start}}
	first, err := p.expect(tokName, "")
	if err != nil {
		return nil, err
	}
	// `{% set ns.attr = value %}` assigns through a namespace object.
	if p.peek().kind == tokOp && p.peek().val == "." {
		p.i++
		attr, err := p.expect(tokName, "")
		if err != nil {
			return nil, err
		}
		n.Namespace, n.Attr = first.val, attr.val
		if _, err := p.expect(tokOp, "="); err != nil {
			return nil, err
		}
		n.Value, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
		_, err = p.expect(tokTagEnd, "")
		return n, err
	}

	n.Targets = []string{first.val}
	for p.accept(tokOp, ",") {
		t, err := p.expect(tokName, "")
		if err != nil {
			return nil, err
		}
		n.Targets = append(n.Targets, t.val)
	}

	if p.accept(tokOp, "=") {
		n.Value, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
		_, err = p.expect(tokTagEnd, "")
		return n, err
	}

	// Block form: `{% set name %}...{% endset %}`, optionally filtered.
	if p.peek().kind == tokOp && p.peek().val == "|" {
		p.i++
		n.Filter, err = p.parseFilterChain(&LiteralExpr{baseNode{start}, ""})
		if err != nil {
			return nil, err
		}
	}
	if _, err := p.expect(tokTagEnd, ""); err != nil {
		return nil, err
	}
	body, _, err := p.parseBody([]string{"endset"})
	if err != nil {
		return nil, err
	}
	n.Body = body
	return n, p.endTag()
}

func (p *parser) parseMacro(start Pos) (Node, error) {
	nameTok, err := p.expect(tokName, "")
	if err != nil {
		return nil, err
	}
	n := &MacroNode{baseNode: baseNode{start}, Name: nameTok.val}
	n.Params, err = p.parseParamList()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tokTagEnd, ""); err != nil {
		return nil, err
	}
	body, _, err := p.parseBody([]string{"endmacro"})
	if err != nil {
		return nil, err
	}
	n.Body = body
	return n, p.endTag()
}

func (p *parser) parseParamList() ([]Param, error) {
	var params []Param
	if _, err := p.expect(tokOp, "("); err != nil {
		return nil, err
	}
	for !p.accept(tokOp, ")") {
		t, err := p.expect(tokName, "")
		if err != nil {
			return nil, err
		}
		pm := Param{Name: t.val}
		if p.accept(tokOp, "=") {
			pm.Default, err = p.parseExpr()
			if err != nil {
				return nil, err
			}
		}
		params = append(params, pm)
		if !p.accept(tokOp, ",") {
			if _, err := p.expect(tokOp, ")"); err != nil {
				return nil, err
			}
			break
		}
	}
	return params, nil
}

func (p *parser) parseCall(start Pos) (Node, error) {
	n := &CallNode{baseNode: baseNode{start}}
	var err error
	if p.peek().kind == tokOp && p.peek().val == "(" {
		n.Params, err = p.parseParamList()
		if err != nil {
			return nil, err
		}
	}
	n.Call, err = p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tokTagEnd, ""); err != nil {
		return nil, err
	}
	body, _, err := p.parseBody([]string{"endcall"})
	if err != nil {
		return nil, err
	}
	n.Body = body
	return n, p.endTag()
}

func (p *parser) parseInclude(start Pos) (Node, error) {
	n := &IncludeNode{baseNode: baseNode{start}, WithContext: true}
	var err error
	if n.Name, err = p.parseExpr(); err != nil {
		return nil, err
	}
	for {
		if p.peek().kind == tokName && p.peek().val == "ignore" {
			p.i++
			if _, err := p.expect(tokName, "missing"); err != nil {
				return nil, err
			}
			n.IgnoreMissing = true
			continue
		}
		if ctx, ok := p.parseContextModifier(); ok {
			n.WithContext = ctx
			continue
		}
		break
	}
	_, err = p.expect(tokTagEnd, "")
	return n, err
}

// parseContextModifier reads a trailing `with context` or `without
// context` and reports which it was.
func (p *parser) parseContextModifier() (bool, bool) {
	t := p.peek()
	if t.kind != tokName || (t.val != "with" && t.val != "without") {
		return false, false
	}
	nt := p.toks[p.i+1]
	if nt.kind != tokName || nt.val != "context" {
		return false, false
	}
	p.i += 2
	return t.val == "with", true
}

func (p *parser) parseImport(start Pos) (Node, error) {
	n := &ImportNode{baseNode: baseNode{start}}
	var err error
	if n.Name, err = p.parseExpr(); err != nil {
		return nil, err
	}
	if _, err := p.expect(tokName, "as"); err != nil {
		return nil, err
	}
	as, err := p.expect(tokName, "")
	if err != nil {
		return nil, err
	}
	n.As = as.val
	if ctx, ok := p.parseContextModifier(); ok {
		n.WithContext = ctx
	}
	_, err = p.expect(tokTagEnd, "")
	return n, err
}

func (p *parser) parseFromImport(start Pos) (Node, error) {
	n := &FromImportNode{baseNode: baseNode{start}}
	var err error
	if n.Name, err = p.parseExpr(); err != nil {
		return nil, err
	}
	if _, err := p.expect(tokName, "import"); err != nil {
		return nil, err
	}
	for {
		if ctx, ok := p.parseContextModifier(); ok {
			n.WithContext = ctx
			break
		}
		t, err := p.expect(tokName, "")
		if err != nil {
			return nil, err
		}
		im := ImportName{Name: t.val, As: t.val}
		if p.peek().kind == tokName && p.peek().val == "as" {
			p.i++
			a, err := p.expect(tokName, "")
			if err != nil {
				return nil, err
			}
			im.As = a.val
		}
		n.Names = append(n.Names, im)
		if !p.accept(tokOp, ",") {
			if ctx, ok := p.parseContextModifier(); ok {
				n.WithContext = ctx
			}
			break
		}
	}
	_, err = p.expect(tokTagEnd, "")
	return n, err
}

func (p *parser) parseExtends(start Pos) (Node, error) {
	n := &ExtendsNode{baseNode: baseNode{start}}
	var err error
	if n.Name, err = p.parseExpr(); err != nil {
		return nil, err
	}
	_, err = p.expect(tokTagEnd, "")
	return n, err
}

func (p *parser) parseBlock(start Pos) (Node, error) {
	nameTok, err := p.expect(tokName, "")
	if err != nil {
		return nil, err
	}
	n := &BlockNode{baseNode: baseNode{start}, Name: nameTok.val}
	if p.peek().kind == tokName && p.peek().val == "scoped" {
		p.i++
		n.Scoped = true
	}
	if _, err := p.expect(tokTagEnd, ""); err != nil {
		return nil, err
	}
	body, _, err := p.parseBody([]string{"endblock"})
	if err != nil {
		return nil, err
	}
	n.Body = body
	// `{% endblock name %}` repeats the name; accept and check it.
	if _, err := p.expect(tokTagStart, ""); err != nil {
		return nil, err
	}
	p.next() // endblock
	if p.peek().kind == tokName {
		closing := p.next()
		if closing.val != n.Name {
			return nil, errorf(closing.pos, "{%% endblock %s %%} does not match {%% block %s %%}", closing.val, n.Name)
		}
	}
	_, err = p.expect(tokTagEnd, "")
	return n, err
}

func (p *parser) parseFilter(start Pos) (Node, error) {
	n := &FilterNode{baseNode: baseNode{start}}
	f, err := p.parseFilterChain(&LiteralExpr{baseNode{start}, ""})
	if err != nil {
		return nil, err
	}
	n.Filter = f
	if _, err := p.expect(tokTagEnd, ""); err != nil {
		return nil, err
	}
	body, _, err := p.parseBody([]string{"endfilter"})
	if err != nil {
		return nil, err
	}
	n.Body = body
	return n, p.endTag()
}

func (p *parser) parseDo(start Pos) (Node, error) {
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tokTagEnd, ""); err != nil {
		return nil, err
	}
	return &DoNode{baseNode{start}, e}, nil
}

func (p *parser) parseWith(start Pos) (Node, error) {
	n := &WithNode{baseNode: baseNode{start}}
	for p.peek().kind == tokName {
		t := p.next()
		if _, err := p.expect(tokOp, "="); err != nil {
			return nil, err
		}
		v, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		n.Targets = append(n.Targets, t.val)
		n.Values = append(n.Values, v)
		if !p.accept(tokOp, ",") {
			break
		}
	}
	if _, err := p.expect(tokTagEnd, ""); err != nil {
		return nil, err
	}
	body, _, err := p.parseBody([]string{"endwith"})
	if err != nil {
		return nil, err
	}
	n.Body = body
	return n, p.endTag()
}

func (p *parser) parseAutoescape(start Pos) (Node, error) {
	if _, err := p.parseExpr(); err != nil {
		return nil, err
	}
	if _, err := p.expect(tokTagEnd, ""); err != nil {
		return nil, err
	}
	body, _, err := p.parseBody([]string{"endautoescape"})
	if err != nil {
		return nil, err
	}
	return &AutoescapeNode{baseNode{start}, body}, p.endTag()
}
