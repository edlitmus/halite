package template

// The expression grammar, in decreasing precedence:
//
//	conditional  := or [ "if" or "else" conditional ]
//	or           := and { "or" and }
//	and          := not { "and" not }
//	not          := "not" not | comparison
//	comparison   := concat { ("==" | "!=" | "<" | "<=" | ">" | ">=" | "in" |
//	                          "not in" | "is" [ "not" ] test) concat }
//	concat       := additive { "~" additive }
//	additive     := multiplicative { ("+" | "-") multiplicative }
//	multiplicative := unary { ("*" | "/" | "//" | "%") unary }
//	unary        := ("-" | "+") unary | power
//	power        := postfix [ "**" unary ]
//	postfix      := primary { "." name | "[" subscript "]" | "(" args ")" }
//	filters      := unary { "|" name [ "(" args ")" ] }
//
// Filters sit above unary rather than inside postfix, which is what makes
// `-5 | abs` evaluate to 5: the filter applies to the negation, not to the
// operand of the negation.
//
// Python's associativity is followed, including `**` binding to the right.

func (p *parser) parseExpr() (Expr, error) { return p.parseConditional() }

func (p *parser) parseConditional() (Expr, error) {
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokName || p.peek().val != "if" {
		return e, nil
	}
	pos := p.next().pos
	cond, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	n := &CondExpr{baseNode{pos}, cond, e, nil}
	if p.peek().kind == tokName && p.peek().val == "else" {
		p.i++
		n.False, err = p.parseConditional()
		if err != nil {
			return nil, err
		}
	}
	return n, nil
}

func (p *parser) parseOr() (Expr, error) {
	e, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokName && p.peek().val == "or" {
		pos := p.next().pos
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		e = &BinaryExpr{baseNode{pos}, "or", e, r}
	}
	return e, nil
}

func (p *parser) parseAnd() (Expr, error) {
	e, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokName && p.peek().val == "and" {
		pos := p.next().pos
		r, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		e = &BinaryExpr{baseNode{pos}, "and", e, r}
	}
	return e, nil
}

func (p *parser) parseNot() (Expr, error) {
	if p.peek().kind == tokName && p.peek().val == "not" {
		pos := p.next().pos
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{baseNode{pos}, "not", x}, nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (Expr, error) {
	e, err := p.parseConcat()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		switch {
		case t.kind == tokOp && (t.val == "==" || t.val == "!=" || t.val == "<" ||
			t.val == "<=" || t.val == ">" || t.val == ">="):
			p.i++
			r, err := p.parseConcat()
			if err != nil {
				return nil, err
			}
			e = &BinaryExpr{baseNode{t.pos}, t.val, e, r}

		case t.kind == tokName && t.val == "in":
			p.i++
			r, err := p.parseConcat()
			if err != nil {
				return nil, err
			}
			e = &BinaryExpr{baseNode{t.pos}, "in", e, r}

		case t.kind == tokName && t.val == "not" &&
			p.toks[p.i+1].kind == tokName && p.toks[p.i+1].val == "in":
			p.i += 2
			r, err := p.parseConcat()
			if err != nil {
				return nil, err
			}
			e = &UnaryExpr{baseNode{t.pos}, "not", &BinaryExpr{baseNode{t.pos}, "in", e, r}}

		case t.kind == tokName && t.val == "is":
			p.i++
			e, err = p.parseTest(e, t.pos)
			if err != nil {
				return nil, err
			}

		default:
			return e, nil
		}
	}
}

// parseTest reads the right side of an `is` operator.
func (p *parser) parseTest(obj Expr, pos Pos) (Expr, error) {
	n := &TestExpr{baseNode: baseNode{pos}, Obj: obj}
	if p.peek().kind == tokName && p.peek().val == "not" {
		p.i++
		n.Not = true
	}
	t := p.peek()
	switch {
	case t.kind == tokName:
		p.i++
		n.Name = t.val
	default:
		return nil, errorf(t.pos, "expected a test name after `is`, found %s", t)
	}

	// A test may take arguments either parenthesised or, for the single
	// argument case, bare: `x is divisibleby 3`.
	if p.peek().kind == tokOp && p.peek().val == "(" {
		p.i++
		for !p.accept(tokOp, ")") {
			a, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			n.Args = append(n.Args, a)
			if !p.accept(tokOp, ",") {
				if _, err := p.expect(tokOp, ")"); err != nil {
					return nil, err
				}
				break
			}
		}
		return n, nil
	}
	if testTakesArgument(n.Name) && startsExpression(p.peek()) {
		a, err := p.parseConcat()
		if err != nil {
			return nil, err
		}
		n.Args = append(n.Args, a)
	}
	return n, nil
}

// testTakesArgument names the tests that accept a bare argument, so that
// `x is defined` does not swallow whatever follows it.
func testTakesArgument(name string) bool {
	switch name {
	case "divisibleby", "eq", "equalto", "ne", "lt", "le", "gt", "ge",
		"sameas", "match", "greaterthan", "lessthan", "in":
		return true
	}
	return false
}

func startsExpression(t token) bool {
	switch t.kind {
	case tokName:
		switch t.val {
		case "and", "or", "not", "if", "else", "is", "in", "recursive":
			return false
		}
		return true
	case tokString, tokInt, tokFloat:
		return true
	case tokOp:
		return t.val == "(" || t.val == "[" || t.val == "{" || t.val == "-"
	}
	return false
}

func (p *parser) parseConcat() (Expr, error) {
	e, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOp && p.peek().val == "~" {
		pos := p.next().pos
		r, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		e = &BinaryExpr{baseNode{pos}, "~", e, r}
	}
	return e, nil
}

func (p *parser) parseAdditive() (Expr, error) {
	e, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOp && (p.peek().val == "+" || p.peek().val == "-") {
		t := p.next()
		r, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		e = &BinaryExpr{baseNode{t.pos}, t.val, e, r}
	}
	return e, nil
}

func (p *parser) parseMultiplicative() (Expr, error) {
	e, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOp {
		v := p.peek().val
		if v != "*" && v != "/" && v != "//" && v != "%" {
			break
		}
		t := p.next()
		r, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		e = &BinaryExpr{baseNode{t.pos}, t.val, e, r}
	}
	return e, nil
}

func (p *parser) parseUnary() (Expr, error) { return p.parseUnaryOperand(true) }

// parseUnaryOperand parses a prefix `-` or `+`. A filter written after a
// negated operand applies to the negation, so `-5 | abs` is 5 rather than
// -5; that is Jinja's rule, and a tree that computes a size or an offset
// depends on it.
func (p *parser) parseUnaryOperand(withFilter bool) (Expr, error) {
	if p.peek().kind == tokOp && (p.peek().val == "-" || p.peek().val == "+") {
		t := p.next()
		x, err := p.parseUnaryOperand(false)
		if err != nil {
			return nil, err
		}
		var node Expr = x
		if t.val == "-" {
			node = &UnaryExpr{baseNode{t.pos}, "-", x}
		}
		if withFilter {
			return p.parseTrailingFilters(node)
		}
		return node, nil
	}
	e, err := p.parsePower()
	if err != nil {
		return nil, err
	}
	if withFilter {
		return p.parseTrailingFilters(e)
	}
	return e, nil
}

// parseTrailingFilters consumes a `| name` chain on an expression that
// parsePostfix did not produce.
func (p *parser) parseTrailingFilters(e Expr) (Expr, error) {
	for p.peek().kind == tokOp && p.peek().val == "|" {
		p.i++
		var err error
		if e, err = p.parseFilterChain(e); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (p *parser) parsePower() (Expr, error) {
	e, err := p.parsePostfix()
	if err != nil {
		return nil, err
	}
	if p.peek().kind == tokOp && p.peek().val == "**" {
		t := p.next()
		// Right associative, as in Python.
		r, err := p.parseUnaryOperand(true)
		if err != nil {
			return nil, err
		}
		return &BinaryExpr{baseNode{t.pos}, "**", e, r}, nil
	}
	return e, nil
}

func (p *parser) parsePostfix() (Expr, error) {
	e, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tokOp {
			return e, nil
		}
		switch t.val {
		case ".":
			p.i++
			nameTok := p.peek()
			// `a.0.1` is two numeric subscripts, and the lexer reads the
			// `0.1` between them as one float, because in every other
			// position that is what it is. Jinja's own lexer has the
			// same seam and splits it the same way.
			if first, second, ok := splitNumericAttrs(nameTok); ok {
				p.i++
				e = &AttrExpr{baseNode{t.pos}, e, first}
				e = &AttrExpr{baseNode{t.pos}, e, second}
				continue
			}
			if nameTok.kind != tokName && nameTok.kind != tokInt {
				return nil, errorf(nameTok.pos, "expected an attribute name after `.`, found %s", nameTok)
			}
			p.i++
			e = &AttrExpr{baseNode{t.pos}, e, nameTok.val}
		case "[":
			p.i++
			e, err = p.parseSubscript(e, t.pos)
			if err != nil {
				return nil, err
			}
		case "(":
			p.i++
			e, err = p.parseCallArgs(e, t.pos)
			if err != nil {
				return nil, err
			}
		default:
			return e, nil
		}
	}
}

func (p *parser) parseSubscript(obj Expr, pos Pos) (Expr, error) {
	// The parts are split on `:`, and an omitted part is a nil entry, so
	// that [::2] is told apart from [:2] by position rather than by
	// arrival order.
	var parts []Expr
	colons := 0

	readPart := func() error {
		if p.peek().kind == tokOp && (p.peek().val == ":" || p.peek().val == "]") {
			parts = append(parts, nil)
			return nil
		}
		e, err := p.parseExpr()
		if err != nil {
			return err
		}
		parts = append(parts, e)
		return nil
	}

	if err := readPart(); err != nil {
		return nil, err
	}
	for p.peek().kind == tokOp && p.peek().val == ":" {
		p.i++
		colons++
		if colons > 2 {
			return nil, errorf(pos, "a slice takes at most a start, a stop, and a step")
		}
		if err := readPart(); err != nil {
			return nil, err
		}
	}
	if _, err := p.expect(tokOp, "]"); err != nil {
		return nil, err
	}

	if colons == 0 {
		if parts[0] == nil {
			// `x[]` with no colon is an empty subscript, which Python and
			// Jinja both reject. Accepting it built an ItemExpr with a nil
			// index that the evaluator then dereferenced.
			return nil, errorf(pos, "a subscript needs an index; write x[0], or x[:] for a slice")
		}
		return &ItemExpr{baseNode{pos}, obj, parts[0]}, nil
	}
	sl := &SliceExpr{baseNode: baseNode{pos}, Obj: obj, Start: parts[0]}
	if len(parts) > 1 {
		sl.Stop = parts[1]
	}
	if len(parts) > 2 {
		sl.Step = parts[2]
	}
	return sl, nil
}

func (p *parser) parseCallArgs(fn Expr, pos Pos) (Expr, error) {
	n := &CallExpr{baseNode: baseNode{pos}, Fn: fn}
	for !p.accept(tokOp, ")") {
		// *args and **kwargs unpacking at a call site.
		if p.peek().kind == tokOp && p.peek().val == "**" {
			p.i++
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			n.StarKw = e
			goto comma
		}
		if p.peek().kind == tokOp && p.peek().val == "*" {
			p.i++
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			n.StarArgs = e
			goto comma
		}
		// A keyword argument is a bare name followed by `=`, which is not
		// otherwise a valid expression start.
		if p.peek().kind == tokName && p.toks[p.i+1].kind == tokOp && p.toks[p.i+1].val == "=" {
			name := p.next().val
			p.i++ // =
			v, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			n.Kwargs = append(n.Kwargs, KwArg{name, v})
			goto comma
		}
		{
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			n.Args = append(n.Args, e)
		}
	comma:
		if !p.accept(tokOp, ",") {
			if _, err := p.expect(tokOp, ")"); err != nil {
				return nil, err
			}
			break
		}
	}
	return n, nil
}

// parseFilterChain reads one filter after a `|`. The postfix loop calls it
// again for each further `|`.
func (p *parser) parseFilterChain(obj Expr) (Expr, error) {
	nameTok, err := p.expect(tokName, "")
	if err != nil {
		return nil, err
	}
	f := &FilterExpr{baseNode: baseNode{nameTok.pos}, Obj: obj, Name: nameTok.val}
	if p.peek().kind == tokOp && p.peek().val == "(" {
		p.i++
		call, err := p.parseCallArgs(&LiteralExpr{baseNode{nameTok.pos}, nil}, nameTok.pos)
		if err != nil {
			return nil, err
		}
		c := call.(*CallExpr)
		f.Args, f.Kwargs = c.Args, c.Kwargs
	}
	return f, nil
}

func (p *parser) parsePrimary() (Expr, error) {
	t := p.peek()
	switch t.kind {
	case tokString:
		p.i++
		// Adjacent string literals concatenate, as in Python.
		s := t.val
		for p.peek().kind == tokString {
			s += p.next().val
		}
		return &LiteralExpr{baseNode{t.pos}, s}, nil

	case tokInt:
		p.i++
		return &LiteralExpr{baseNode{t.pos}, t.num}, nil

	case tokFloat:
		p.i++
		return &LiteralExpr{baseNode{t.pos}, t.num}, nil

	case tokName:
		switch t.val {
		case "true", "True":
			p.i++
			return &LiteralExpr{baseNode{t.pos}, true}, nil
		case "false", "False":
			p.i++
			return &LiteralExpr{baseNode{t.pos}, false}, nil
		case "none", "None":
			p.i++
			return &LiteralExpr{baseNode{t.pos}, nil}, nil
		}
		p.i++
		return &NameExpr{baseNode{t.pos}, t.val}, nil

	case tokOp:
		switch t.val {
		case "(":
			p.i++
			if p.accept(tokOp, ")") {
				return &TupleExpr{baseNode{t.pos}, nil}, nil
			}
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if p.peek().kind == tokOp && p.peek().val == "," {
				items := []Expr{e}
				for p.accept(tokOp, ",") {
					if p.peek().kind == tokOp && p.peek().val == ")" {
						break
					}
					x, err := p.parseExpr()
					if err != nil {
						return nil, err
					}
					items = append(items, x)
				}
				if _, err := p.expect(tokOp, ")"); err != nil {
					return nil, err
				}
				return &TupleExpr{baseNode{t.pos}, items}, nil
			}
			if _, err := p.expect(tokOp, ")"); err != nil {
				return nil, err
			}
			return e, nil

		case "[":
			p.i++
			n := &ListExpr{baseNode: baseNode{t.pos}}
			for !p.accept(tokOp, "]") {
				e, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				n.Items = append(n.Items, e)
				if !p.accept(tokOp, ",") {
					if _, err := p.expect(tokOp, "]"); err != nil {
						return nil, err
					}
					break
				}
			}
			return n, nil

		case "{":
			p.i++
			n := &DictExpr{baseNode: baseNode{t.pos}}
			for !p.accept(tokOp, "}") {
				k, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				if _, err := p.expect(tokOp, ":"); err != nil {
					return nil, err
				}
				v, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				n.Keys = append(n.Keys, k)
				n.Vals = append(n.Vals, v)
				if !p.accept(tokOp, ",") {
					if _, err := p.expect(tokOp, "}"); err != nil {
						return nil, err
					}
					break
				}
			}
			return n, nil
		}
	}
	return nil, errorf(t.pos, "unexpected %s in an expression", t)
}

// splitNumericAttrs reads a float token that is really two numeric
// attribute names, as in `a.0.1`.
func splitNumericAttrs(t token) (first, second string, ok bool) {
	if t.kind != tokFloat {
		return "", "", false
	}
	dot := -1
	for i := 0; i < len(t.val); i++ {
		if t.val[i] == '.' {
			dot = i
			break
		}
	}
	if dot <= 0 || dot == len(t.val)-1 {
		return "", "", false
	}
	a, b := t.val[:dot], t.val[dot+1:]
	if !allDigits(a) || !allDigits(b) {
		return "", "", false
	}
	return a, b, true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
