package template

// This file measures the trees the parser builds, so that no walk of one
// can recurse further than maxExprDepth.
//
// exprChildren and nodeParts are the one place that knows which fields of
// a node hold further nodes. They are two paths that must agree with
// ast.go, and a field added there and forgotten here would be a subtree
// nobody measures; TestDepthWalkCoversEveryField reads ast.go with
// go/parser and fails on any node type or field these do not visit.

// checkExprHeight reports the first expression in body that is taller
// than limit. Statement nesting is already bounded by the parser at
// MaxDepth, so the recursion over bodies here is bounded too.
func checkExprHeight(body []Node, limit int) error {
	for _, n := range body {
		exprs, bodies := nodeParts(n)
		for _, e := range exprs {
			if deep := tallerThan(e, 1, limit); deep != nil {
				return errorf(deep.Pos(), "expression nests deeper than %d levels", limit)
			}
		}
		for _, b := range bodies {
			if err := checkExprHeight(b, limit); err != nil {
				return err
			}
		}
	}
	return nil
}

// tallerThan returns a node lying deeper than limit below e, which is at
// depth, or nil. It stops descending at the limit, so its own recursion
// is bounded by the thing it measures.
func tallerThan(e Expr, depth, limit int) Expr {
	if e == nil {
		return nil
	}
	if depth > limit {
		return e
	}
	for _, c := range exprChildren(e) {
		if deep := tallerThan(c, depth+1, limit); deep != nil {
			return deep
		}
	}
	return nil
}

// exprChildren lists the sub-expressions of e. A nil entry is an omitted
// part, such as the stop of `x[1:]`, and tallerThan skips it.
func exprChildren(e Expr) []Expr {
	switch t := e.(type) {
	case *LiteralExpr, *NameExpr:
		return nil
	case *AttrExpr:
		return []Expr{t.Obj}
	case *ItemExpr:
		return []Expr{t.Obj, t.Index}
	case *SliceExpr:
		return []Expr{t.Obj, t.Start, t.Stop, t.Step}
	case *CallExpr:
		out := append([]Expr{t.Fn, t.StarArgs, t.StarKw}, t.Args...)
		return appendKwargs(out, t.Kwargs)
	case *FilterExpr:
		out := append([]Expr{t.Obj}, t.Args...)
		return appendKwargs(out, t.Kwargs)
	case *TestExpr:
		return append([]Expr{t.Obj}, t.Args...)
	case *BinaryExpr:
		return []Expr{t.L, t.R}
	case *UnaryExpr:
		return []Expr{t.X}
	case *CompareExpr:
		return append([]Expr{t.First}, t.Rest...)
	case *CondExpr:
		return []Expr{t.Cond, t.True, t.False}
	case *ListExpr:
		return t.Items
	case *TupleExpr:
		return t.Items
	case *DictExpr:
		return append(append([]Expr{}, t.Keys...), t.Vals...)
	}
	// An expression type this switch does not know is a programming
	// error, and the coverage test exists to make it impossible; it is
	// treated as a leaf rather than a panic.
	return nil
}

func appendKwargs(out []Expr, kws []KwArg) []Expr {
	for _, kw := range kws {
		out = append(out, kw.Val)
	}
	return out
}

func appendParams(out []Expr, ps []Param) []Expr {
	for _, p := range ps {
		out = append(out, p.Default)
	}
	return out
}

// nodeParts lists the expressions a statement holds directly and the
// bodies nested inside it.
func nodeParts(n Node) ([]Expr, [][]Node) {
	switch t := n.(type) {
	case *TextNode, *BreakNode, *ContinueNode:
		return nil, nil
	case *OutputNode:
		return []Expr{t.Expr}, nil
	case *IfNode:
		bodies := append([][]Node{}, t.Blocks...)
		return t.Conds, append(bodies, t.Else)
	case *ForNode:
		return []Expr{t.Iter, t.Cond}, [][]Node{t.Body, t.Else}
	case *SetNode:
		return []Expr{t.Value, t.Filter}, [][]Node{t.Body}
	case *MacroNode:
		return appendParams(nil, t.Params), [][]Node{t.Body}
	case *CallNode:
		return appendParams([]Expr{t.Call}, t.Params), [][]Node{t.Body}
	case *IncludeNode:
		return []Expr{t.Name}, nil
	case *ImportNode:
		return []Expr{t.Name}, nil
	case *DataImportNode:
		return []Expr{t.Name}, nil
	case *FromImportNode:
		return []Expr{t.Name}, nil
	case *ExtendsNode:
		return []Expr{t.Name}, nil
	case *BlockNode:
		return nil, [][]Node{t.Body}
	case *FilterNode:
		return []Expr{t.Filter}, [][]Node{t.Body}
	case *DoNode:
		return []Expr{t.Expr}, nil
	case *WithNode:
		return t.Values, [][]Node{t.Body}
	case *AutoescapeNode:
		return nil, [][]Node{t.Body}
	}
	return nil, nil
}
