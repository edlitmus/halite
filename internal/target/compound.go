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
	// terms is every leaf compiled, in order, with nodegroups expanded.
	terms  []Term
	src    string
	toks   []compoundToken
	i      int
	groups Nodegroups
	// depth is how many `(` and `not` enclose the token being parsed.
	// See maxCompoundDepth.
	depth int
}

// maxCompoundDepth bounds how deeply an expression may nest, counting
// each `(` and each `not` as one level.
//
// The parser is recursive descent and the tree it builds is evaluated
// recursively, so nesting is stack. A Go stack overflow is a fatal error
// rather than a panic: nothing recovers it, net/http included, so before
// this bound one deeply nested target from one API caller ended the hub
// (DIVERGENCE 5.291). Measured on darwin/arm64 with go1.27.2 and the
// stack capped at 4 MiB, 10,000 `(` parsed and 15,000 overflowed: some
// 300 to 400 bytes a level, so a few megabytes of `(` would exhaust the
// default 1 GiB stack, well inside the API's 64 MiB body limit.
//
// 100 is chosen as far above anything a person writes and far below
// anything that costs stack. A hand-written target nests a handful of
// levels. Nodegroup expansion wraps each reference in one pair of
// parentheses and references nest at most ten deep (resolveNodegroup),
// so a fully nested nodegroup costs eleven. At 100 levels the parser
// uses some tens of kilobytes of stack.
//
// An N@ term inside a compound is parsed by a fresh parser at depth 0
// (compileTerm), not at the enclosing depth. That is deliberate: a
// nodegroup is checked once at load by ValidateNodegroups, at depth 0,
// and if use counted from the enclosing depth a group that passed at
// load could fail at use. The stack stays bounded: the expansion has no
// N@ left in it, so the fresh parser never starts another, and the worst
// case is twice the bound.
//
// Length is deliberately not bounded here. A flat chain of `or` and
// `and` is a loop in the parser and a slice in the tree (anyNode,
// allNode), so its cost is linear in time and memory and costs no stack;
// a long target is a list of node names, which a large estate legitimately
// has, and refusing it would be refusing a valid target for no crash
// behind it.
const maxCompoundDepth = 100

// enter records one more level of nesting at t, refusing it past the
// bound. Every caller pairs it with leave.
func (p *parser) enter(t compoundToken) error {
	if p.depth >= maxCompoundDepth {
		return &Error{Expr: p.src, Col: t.col, Msg: fmt.Sprintf("the expression nests deeper than %d levels of `(` and `not`", maxCompoundDepth)}
	}
	p.depth++
	return nil
}

func (p *parser) leave() { p.depth-- }

// anyNode and allNode are an `or` chain and an `and` chain as a list
// rather than as one closure inside the next.
//
// They used to be nested closures, ((a or b) or c) or ..., which the
// parser built in a loop and so never looked recursive, but Match then
// recursed once per term. With the stack capped at 4 MiB a chain of
// 25,000 terms evaluated and one of 30,000 overflowed, some 150 bytes a
// term, which puts the default 1 GiB stack at a few tens of megabytes of
// `a or a or ...`: inside the API's 64 MiB body limit, and a crash at
// match time rather than parse time. The tree is now as deep as the
// expression's nesting, which maxCompoundDepth bounds. Evaluation order
// and short-circuiting are left to right, as before.
type anyNode []node

func (a anyNode) match(n Node) bool {
	for _, x := range a {
		if x.match(n) {
			return true
		}
	}
	return false
}

type allNode []node

func (a allNode) match(n Node) bool {
	for _, x := range a {
		if !x.match(n) {
			return false
		}
	}
	return true
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
	first, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	terms := anyNode{first}
	for {
		t, ok := p.peek()
		if !ok || !strings.EqualFold(t.text, "or") {
			break
		}
		p.i++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		terms = append(terms, right)
	}
	if len(terms) == 1 {
		return first, nil
	}
	return terms, nil
}

func (p *parser) parseAnd() (node, error) {
	first, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	terms := allNode{first}
	for {
		t, ok := p.peek()
		// Salt writes `and not x` as two words; both spellings reach the
		// same tree because `not` is handled by parseNot.
		if !ok || !strings.EqualFold(t.text, "and") {
			break
		}
		p.i++
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		terms = append(terms, right)
	}
	if len(terms) == 1 {
		return first, nil
	}
	return terms, nil
}

func (p *parser) parseNot() (node, error) {
	t, ok := p.peek()
	if ok && strings.EqualFold(t.text, "not") {
		if err := p.enter(t); err != nil {
			return nil, err
		}
		p.i++
		inner, err := p.parseNot()
		p.leave()
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
		if err := p.enter(t); err != nil {
			return nil, err
		}
		p.i++
		inner, err := p.parseOr()
		p.leave()
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
		p.terms = append(p.terms, Term{Kind: Glob})
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
		p.terms = append(p.terms, Term{Kind: Glob})
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
		// The group's own terms are this expression's terms. Without
		// this line a nodegroup is a hole in anything that judges a
		// target by what it consults, which is what it was: see Term.
		p.terms = append(p.terms, sub.terms...)
		return n, nil
	}

	leaf, err := compileLeaf(kind, rest)
	if err != nil {
		return nil, withCol(err, t.col)
	}
	p.terms = append(p.terms, Term{Kind: kind, Key: leafKey(kind, rest)})
	return leaf, nil
}

func withCol(err error, col int) error {
	if te, ok := err.(*Error); ok && te.Col == 0 {
		te.Col = col
		return te
	}
	return err
}
