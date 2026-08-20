package target

import (
	"fmt"
	"strings"
)

// The compound grammar of SPEC section 8.2, written out rather than left
// to a regular expression, because Salt's compound matcher has had
// precedence bugs and a target expression that quietly widens is how a job
// reaches a host it was never meant for.
//
//	expr    := orExpr
//	orExpr  := andExpr ( "or" andExpr )*
//	andExpr := notExpr ( "and" notExpr )*
//	notExpr := "not" notExpr | primary
//	primary := "(" expr ")" | typed | glob
//	typed   := TYPE "@" VALUE
//	TYPE    := "G" | "P" | "I" | "J" | "L" | "S" | "E" | "N"
//
// Precedence is not, then and, then or. Parentheses group.

type compoundToken struct {
	text string
	col  int
}

type parser struct {
	src    string
	toks   []compoundToken
	i      int
	groups Nodegroups
}

func (p *parser) parse() (node, error) {
	p.tokenize()
	if len(p.toks) == 0 {
		return nil, &Error{Expr: p.src, Msg: "the expression is empty"}
	}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.i < len(p.toks) {
		t := p.toks[p.i]
		msg := fmt.Sprintf("unexpected %q", t.text)
		if t.text == ")" {
			msg = "unmatched `)`"
		}
		return nil, &Error{Expr: p.src, Col: t.col, Msg: msg}
	}
	return n, nil
}

// tokenize splits on whitespace and on parentheses, keeping each token's
// column so a diagnostic can point at it.
//
// A parenthesis groups only when it opens a token. Once a token has begun,
// parentheses belong to it and are tracked to their close, because a regex
// target is full of them: `E@^(web|db)[0-9]` is one term, not three, and
// splitting it produced an error about a type sigil with no value.
func (p *parser) tokenize() {
	start := -1
	depth := 0
	flush := func(end int) {
		if start >= 0 {
			p.toks = append(p.toks, compoundToken{text: p.src[start:end], col: start + 1})
			start = -1
			depth = 0
		}
	}
	for i := 0; i < len(p.src); i++ {
		switch c := p.src[i]; {
		case c == ' ' || c == '\t':
			if depth > 0 {
				// Whitespace inside a term's parentheses is part of it,
				// which a regex alternation may legitimately contain.
				continue
			}
			flush(i)
		case c == '(' && start < 0:
			p.toks = append(p.toks, compoundToken{text: "(", col: i + 1})
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		case c == ')':
			flush(i)
			p.toks = append(p.toks, compoundToken{text: ")", col: i + 1})
		default:
			if start < 0 {
				start = i
			}
		}
	}
	flush(len(p.src))
}

func (p *parser) peek() (compoundToken, bool) {
	if p.i >= len(p.toks) {
		return compoundToken{}, false
	}
	return p.toks[p.i], true
}

func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		t, ok := p.peek()
		if !ok || !strings.EqualFold(t.text, "or") {
			return left, nil
		}
		p.i++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = funcNode(func(n Node) bool { return l.match(n) || r.match(n) })
	}
}

func (p *parser) parseAnd() (node, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for {
		t, ok := p.peek()
		if !ok {
			return left, nil
		}
		// Salt writes `and not x` as two words; both spellings reach the
		// same tree because `not` is handled by parseNot.
		if !strings.EqualFold(t.text, "and") {
			return left, nil
		}
		p.i++
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = funcNode(func(n Node) bool { return l.match(n) && r.match(n) })
	}
}

func (p *parser) parseNot() (node, error) {
	t, ok := p.peek()
	if ok && strings.EqualFold(t.text, "not") {
		p.i++
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return funcNode(func(n Node) bool { return !inner.match(n) }), nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (node, error) {
	t, ok := p.peek()
	if !ok {
		col := len(p.src) + 1
		if len(p.toks) > 0 {
			last := p.toks[len(p.toks)-1]
			col = last.col + len(last.text)
		}
		return nil, &Error{Expr: p.src, Col: col, Msg: "the expression ends where a target was expected"}
	}

	switch {
	case t.text == "(":
		p.i++
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		close, ok := p.peek()
		if !ok || close.text != ")" {
			return nil, &Error{Expr: p.src, Col: t.col, Msg: "this `(` is never closed"}
		}
		p.i++
		return inner, nil

	case t.text == ")":
		return nil, &Error{Expr: p.src, Col: t.col, Msg: "unmatched `)`"}

	case strings.EqualFold(t.text, "and") || strings.EqualFold(t.text, "or"):
		return nil, &Error{Expr: p.src, Col: t.col, Msg: fmt.Sprintf("%q needs a target on its left", t.text)}
	}

	p.i++
	return p.compileTerm(t)
}

// compileTerm builds a leaf from a typed term or a bare glob.
func (p *parser) compileTerm(t compoundToken) (node, error) {
	at := strings.Index(t.text, "@")
	if at <= 0 {
		leaf, err := compileLeaf(Glob, t.text)
		if err != nil {
			return nil, withCol(err, t.col)
		}
		return leaf, nil
	}

	letter := t.text[:at]
	rest := t.text[at+1:]
	kind, ok := kindLetters[letter]
	if !ok {
		// An `@` that is not a type sigil belongs to the value, which is
		// how an email-like node ID or a grain value containing @ is
		// written.
		leaf, err := compileLeaf(Glob, t.text)
		if err != nil {
			return nil, withCol(err, t.col)
		}
		return leaf, nil
	}
	if rest == "" {
		return nil, &Error{Expr: p.src, Col: t.col, Msg: fmt.Sprintf("%s@ has no value", letter)}
	}

	if kind == Nodegroup {
		expanded, err := resolveNodegroup(rest, p.groups, 0, nil)
		if err != nil {
			return nil, withCol(err, t.col)
		}
		sub := &parser{src: expanded, groups: p.groups}
		n, err := sub.parse()
		if err != nil {
			return nil, err
		}
		return n, nil
	}

	leaf, err := compileLeaf(kind, rest)
	if err != nil {
		return nil, withCol(err, t.col)
	}
	return leaf, nil
}

func withCol(err error, col int) error {
	if te, ok := err.(*Error); ok && te.Col == 0 {
		te.Col = col
		return te
	}
	return err
}
