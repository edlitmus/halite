package template

import "fmt"

// Error is a template failure with a position. Every limit, every
// undefined name, and every syntax error produces one, because a
// templated highstate that fails without a position is the single most
// familiar misery in operating Salt.
type Error struct {
	Pos   Pos
	Msg   string
	Cause error
}

func (e *Error) Error() string {
	if e.Pos.Line == 0 && e.Pos.File == "" {
		return e.Msg
	}
	return fmt.Sprintf("%s: %s", e.Pos, e.Msg)
}

func (e *Error) Unwrap() error { return e.Cause }

func errorf(pos Pos, format string, args ...any) *Error {
	return &Error{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

// ---- statements ----

// Node is a statement in a template body.
type Node interface{ position() Pos }

type baseNode struct{ P Pos }

func (b baseNode) position() Pos { return b.P }

// TextNode is a literal span between tags.
type TextNode struct {
	baseNode
	Text string
}

// OutputNode is `{{ expr }}`.
type OutputNode struct {
	baseNode
	Expr Expr
}

// IfNode holds the condition/body pairs of an if/elif chain plus its else.
type IfNode struct {
	baseNode
	Conds  []Expr
	Blocks [][]Node
	Else   []Node
}

// ForNode is `{% for targets in iter %}`, with Jinja's inline `if` filter,
// its `else` branch for an empty loop, and `recursive`.
type ForNode struct {
	baseNode
	Targets   []string
	Iter      Expr
	Cond      Expr
	Body      []Node
	Else      []Node
	Recursive bool
}

// SetNode is `{% set a = expr %}` or `{% set a %}body{% endset %}`.
type SetNode struct {
	baseNode
	Targets []string
	Value   Expr
	Body    []Node
	Filter  Expr // a block set may pipe its body through filters
	// Namespace and Attr are set for `{% set ns.attr = expr %}`.
	Namespace string
	Attr      string
}

// MacroNode defines a macro.
type MacroNode struct {
	baseNode
	Name     string
	Params   []Param
	Body     []Node
	Variadic bool
}

// Param is one macro parameter with its optional default.
type Param struct {
	Name    string
	Default Expr
}

// CallNode is `{% call macro(...) %}body{% endcall %}`, which makes the
// body available to the macro as caller().
type CallNode struct {
	baseNode
	Params []Param
	Call   Expr
	Body   []Node
}

// IncludeNode is `{% include expr %}`.
type IncludeNode struct {
	baseNode
	Name          Expr
	IgnoreMissing bool
	WithContext   bool
}

// ImportNode is `{% import expr as name %}`.
type ImportNode struct {
	baseNode
	Name        Expr
	As          string
	WithContext bool
}

// FromImportNode is `{% from expr import a, b as c %}`.
type FromImportNode struct {
	baseNode
	Name        Expr
	Names       []ImportName
	WithContext bool
}

// ImportName is one name in a from-import, with its optional alias.
type ImportName struct{ Name, As string }

// ExtendsNode is `{% extends expr %}`.
type ExtendsNode struct {
	baseNode
	Name Expr
}

// BlockNode is `{% block name %}`, the unit of template inheritance.
type BlockNode struct {
	baseNode
	Name   string
	Body   []Node
	Scoped bool
}

// FilterNode is `{% filter f %}body{% endfilter %}`.
type FilterNode struct {
	baseNode
	Filter Expr
	Body   []Node
}

// DoNode is `{% do expr %}`: evaluate for effect, output nothing.
type DoNode struct {
	baseNode
	Expr Expr
}

// WithNode is `{% with a = 1 %}`, a scope that ends at endwith.
type WithNode struct {
	baseNode
	Targets []string
	Values  []Expr
	Body    []Node
}

// AutoescapeNode is parsed and its body rendered. Escaping itself is a
// no-op: SLS output is not HTML, and escaping it would corrupt it. The
// explicit `escape` filter is available where a caller genuinely wants it.
type AutoescapeNode struct {
	baseNode
	Body []Node
}

// ---- expressions ----

// Expr is an expression node.
type Expr interface{ position() Pos }

// LiteralExpr is a constant.
type LiteralExpr struct {
	baseNode
	Val any
}

// NameExpr is a variable reference.
type NameExpr struct {
	baseNode
	Name string
}

// AttrExpr is `obj.attr`.
type AttrExpr struct {
	baseNode
	Obj  Expr
	Attr string
}

// ItemExpr is `obj[index]`.
type ItemExpr struct {
	baseNode
	Obj   Expr
	Index Expr
}

// SliceExpr is `obj[start:stop:step]`.
type SliceExpr struct {
	baseNode
	Obj               Expr
	Start, Stop, Step Expr
}

// KwArg is one keyword argument at a call site.
type KwArg struct {
	Name string
	Val  Expr
}

// CallExpr is a function or macro call.
type CallExpr struct {
	baseNode
	Fn       Expr
	Args     []Expr
	Kwargs   []KwArg
	StarArgs Expr
	StarKw   Expr
}

// FilterExpr is `expr | name(args)`.
type FilterExpr struct {
	baseNode
	Obj    Expr
	Name   string
	Args   []Expr
	Kwargs []KwArg
}

// TestExpr is `expr is name(args)`, with Not set for `is not`.
type TestExpr struct {
	baseNode
	Obj  Expr
	Name string
	Args []Expr
	Not  bool
}

// BinaryExpr is an infix operation.
type BinaryExpr struct {
	baseNode
	Op   string
	L, R Expr
}

// UnaryExpr is a prefix operation: `-x` or `not x`.
type UnaryExpr struct {
	baseNode
	Op string
	X  Expr
}

// CondExpr is `a if c else b`.
type CondExpr struct {
	baseNode
	Cond, True, False Expr
}

// ListExpr is a list literal.
type ListExpr struct {
	baseNode
	Items []Expr
}

// TupleExpr is a tuple literal, which Jinja treats as a list everywhere
// that matters here.
type TupleExpr struct {
	baseNode
	Items []Expr
}

// DictExpr is a dict literal. Order is preserved, matching the ordered
// mapping the rest of halite uses.
type DictExpr struct {
	baseNode
	Keys, Vals []Expr
}
